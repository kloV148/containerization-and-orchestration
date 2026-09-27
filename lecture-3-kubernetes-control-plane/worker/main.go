// The worker is test infrastructure for Lab 3; the lab focuses on Kubernetes.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const schema = `
CREATE TABLE IF NOT EXISTS orders (
    id BIGSERIAL PRIMARY KEY,
    item TEXT NOT NULL CHECK (char_length(item) BETWEEN 1 AND 200),
    quantity INTEGER NOT NULL CHECK (quantity BETWEEN 1 AND 1000),
    processed BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at TIMESTAMPTZ
)`

type orderStore interface {
	ProcessNext(context.Context) (int64, bool, error)
}

type postgresStore struct {
	db *sql.DB
}

// ProcessNext claims and completes one order in a single statement. SKIP LOCKED
// lets multiple worker replicas process different rows without blocking or
// processing the same order twice.
func (s *postgresStore) ProcessNext(ctx context.Context) (int64, bool, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		WITH next_order AS (
			SELECT id
			FROM orders
			WHERE processed = FALSE
			ORDER BY id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE orders
		SET processed = TRUE, processed_at = NOW()
		FROM next_order
		WHERE orders.id = next_order.id
		RETURNING orders.id`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

type worker struct {
	logger    *slog.Logger
	store     orderStore
	processed prometheus.Counter
	errors    prometheus.Counter
}

func newWorker(logger *slog.Logger, store orderStore, registry *prometheus.Registry) *worker {
	w := &worker{
		logger: logger,
		store:  store,
		processed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "shop_worker_orders_processed_total",
			Help: "Orders successfully processed by the worker.",
		}),
		errors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "shop_worker_errors_total",
			Help: "Database processing errors encountered by the worker.",
		}),
	}
	registry.MustRegister(w.processed, w.errors)
	return w
}

func (w *worker) processOnce(ctx context.Context) bool {
	id, found, err := w.store.ProcessNext(ctx)
	if err != nil {
		w.errors.Inc()
		w.logger.ErrorContext(ctx, "process order failed", "error", err)
		return false
	}
	if found {
		w.processed.Inc()
		w.logger.InfoContext(ctx, "order processed", "order_id", id)
	}
	return found
}

func (w *worker) run(ctx context.Context, interval time.Duration) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			// Drain queued orders immediately. Poll only when the queue is empty
			// or PostgreSQL temporarily returned an error.
			if w.processOnce(ctx) {
				timer.Reset(0)
			} else {
				timer.Reset(interval)
			}
		}
	}
}

func healthHandler(healthFail bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if healthFail {
			http.Error(w, "unhealthy", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "ok\n")
	}
}

func newHandler(healthFail bool, registry *prometheus.Registry) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /health", healthHandler(healthFail))
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	return mux
}

func listenPort() string {
	if port := os.Getenv("PORT"); port != "" {
		return port
	}
	return "8080"
}

func pollInterval() time.Duration {
	if raw := os.Getenv("POLL_INTERVAL"); raw != "" {
		if interval, err := time.ParseDuration(raw); err == nil && interval > 0 {
			return interval
		}
	}
	return time.Second
}

func databaseURL() string {
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		return dsn
	}
	value := func(name, fallback string) string {
		if result := os.Getenv(name); result != "" {
			return result
		}
		return fallback
	}
	dsn := &url.URL{
		Scheme: "postgres",
		Host:   value("PGHOST", "postgres-rw") + ":" + value("PGPORT", "5432"),
		Path:   value("PGDATABASE", "app"),
	}
	dsn.User = url.UserPassword(value("PGUSER", "app"), os.Getenv("PGPASSWORD"))
	query := dsn.Query()
	query.Set("sslmode", value("PGSSLMODE", "disable"))
	dsn.RawQuery = query.Encode()
	return dsn.String()
}

func openPostgres(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create orders table: %w", err)
	}
	return db, nil
}

func envBool(name string) bool {
	value, err := strconv.ParseBool(os.Getenv(name))
	return err == nil && value
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	db, err := openPostgres(connectCtx, databaseURL())
	cancel()
	if err != nil {
		logger.Error("database initialization failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	registry := prometheus.NewRegistry()
	processor := newWorker(logger, &postgresStore{db: db}, registry)
	go processor.run(ctx, pollInterval())

	server := &http.Server{
		Addr:              ":" + listenPort(),
		Handler:           newHandler(envBool("HEALTH_FAIL"), registry),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	logger.Info("worker started", "port", listenPort(), "poll_interval", pollInterval())
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("http server failed", "error", err)
		os.Exit(1)
	}
}
