// The API is test infrastructure for Lab 3; the lab focuses on Kubernetes.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

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

type order struct {
	ID          int64      `json:"id"`
	Item        string     `json:"item"`
	Quantity    int        `json:"quantity"`
	Processed   bool       `json:"processed"`
	CreatedAt   time.Time  `json:"createdAt"`
	ProcessedAt *time.Time `json:"processedAt,omitempty"`
}

type createOrderInput struct {
	Item     string `json:"item"`
	Quantity int    `json:"quantity"`
}

type orderStore interface {
	CreateOrder(context.Context, createOrderInput) (order, error)
	ListOrders(context.Context) ([]order, error)
}

type postgresStore struct {
	db *sql.DB
}

func (s *postgresStore) CreateOrder(ctx context.Context, input createOrderInput) (order, error) {
	var result order
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO orders (item, quantity)
		VALUES ($1, $2)
		RETURNING id, item, quantity, processed, created_at, processed_at`,
		input.Item, input.Quantity,
	).Scan(&result.ID, &result.Item, &result.Quantity, &result.Processed, &result.CreatedAt, &result.ProcessedAt)
	return result, err
}

func (s *postgresStore) ListOrders(ctx context.Context) ([]order, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, item, quantity, processed, created_at, processed_at
		FROM orders
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	orders := make([]order, 0)
	for rows.Next() {
		var current order
		if err := rows.Scan(&current.ID, &current.Item, &current.Quantity, &current.Processed, &current.CreatedAt, &current.ProcessedAt); err != nil {
			return nil, err
		}
		orders = append(orders, current)
	}
	return orders, rows.Err()
}

type app struct {
	logger        *slog.Logger
	store         orderStore
	healthFail    bool
	requests      *prometheus.CounterVec
	duration      *prometheus.HistogramVec
	ordersCreated prometheus.Counter
}

func newApp(logger *slog.Logger, store orderStore, healthFail bool) (*app, http.Handler) {
	registry := prometheus.NewRegistry()
	a := &app{
		logger:     logger,
		store:      store,
		healthFail: healthFail,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "shop_http_requests_total", Help: "Completed API requests.",
		}, []string{"method", "path", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "shop_http_request_duration_seconds", Help: "API request latency in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "path", "code"}),
		ordersCreated: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "shop_orders_created_total", Help: "Orders successfully created by the API.",
		}),
	}
	registry.MustRegister(a.requests, a.duration, a.ordersCreated)

	mux := http.NewServeMux()
	mux.Handle("GET /health", a.instrument("/health", http.HandlerFunc(a.health)))
	mux.Handle("POST /order", a.instrument("/order", http.HandlerFunc(a.createOrder)))
	mux.Handle("GET /orders", a.instrument("/orders", http.HandlerFunc(a.listOrders)))
	// Prometheus scrapes are not counted as user traffic.
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	return a, mux
}

func (a *app) instrument(path string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		code := strconv.Itoa(recorder.status)
		a.requests.WithLabelValues(r.Method, path, code).Inc()
		a.duration.WithLabelValues(r.Method, path, code).Observe(time.Since(start).Seconds())
		a.logger.InfoContext(r.Context(), "request completed",
			"method", r.Method,
			"path", path,
			"status", recorder.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusRecorder) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
		w.ResponseWriter.WriteHeader(code)
	}
}

func (w *statusRecorder) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (a *app) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if a.healthFail {
		http.Error(w, "unhealthy", http.StatusServiceUnavailable)
		return
	}
	_, _ = io.WriteString(w, "ok\n")
}

func (a *app) createOrder(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var input createOrderInput
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "request body must contain a valid JSON order")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "request body must contain exactly one JSON object")
		return
	}
	input.Item = strings.TrimSpace(input.Item)
	if input.Item == "" || utf8.RuneCountInString(input.Item) > 200 {
		writeError(w, http.StatusBadRequest, "item must contain between 1 and 200 characters")
		return
	}
	if input.Quantity < 1 || input.Quantity > 1000 {
		writeError(w, http.StatusBadRequest, "quantity must be between 1 and 1000")
		return
	}

	created, err := a.store.CreateOrder(r.Context(), input)
	if err != nil {
		a.logger.ErrorContext(r.Context(), "create order failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "database is unavailable")
		return
	}
	a.ordersCreated.Inc()
	writeJSON(w, http.StatusCreated, created)
}

func (a *app) listOrders(w http.ResponseWriter, r *http.Request) {
	orders, err := a.store.ListOrders(r.Context())
	if err != nil {
		a.logger.ErrorContext(r.Context(), "list orders failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "database is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, orders)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func listenPort() string {
	if port := os.Getenv("PORT"); port != "" {
		return port
	}
	return "8080"
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

	_, handler := newApp(logger, &postgresStore{db: db}, envBool("HEALTH_FAIL"))
	server := &http.Server{
		Addr:              ":" + listenPort(),
		Handler:           handler,
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

	logger.Info("api listening", "port", listenPort(), "health_fail", envBool("HEALTH_FAIL"))
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("http server failed", "error", err)
		os.Exit(1)
	}
}
