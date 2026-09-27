package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type memoryStore struct {
	id    int64
	found bool
	err   error
}

func (s *memoryStore) ProcessNext(context.Context) (int64, bool, error) {
	return s.id, s.found, s.err
}

func TestHealth(t *testing.T) {
	tests := []struct {
		name       string
		healthFail bool
		wantStatus int
		wantBody   string
	}{
		{name: "healthy", wantStatus: http.StatusOK, wantBody: "ok\n"},
		{name: "forced failure", healthFail: true, wantStatus: http.StatusServiceUnavailable, wantBody: "unhealthy\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			healthHandler(tt.healthFail).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
			if response.Code != tt.wantStatus || response.Body.String() != tt.wantBody {
				t.Fatalf("response = (%d, %q), want (%d, %q)", response.Code, response.Body.String(), tt.wantStatus, tt.wantBody)
			}
		})
	}
}

func TestProcessOnceRecordsMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	w := newWorker(logger, &memoryStore{id: 42, found: true}, registry)
	if !w.processOnce(context.Background()) {
		t.Fatal("processOnce() = false, want true")
	}

	response := httptest.NewRecorder()
	newHandler(false, registry).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(response.Body.String(), "shop_worker_orders_processed_total 1") {
		t.Fatalf("processed metric is missing: %s", response.Body.String())
	}
}

func TestProcessOnceHandlesEmptyQueueAndErrors(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	if newWorker(logger, &memoryStore{}, prometheus.NewRegistry()).processOnce(context.Background()) {
		t.Fatal("empty queue was reported as processed")
	}
	if newWorker(logger, &memoryStore{err: errors.New("down")}, prometheus.NewRegistry()).processOnce(context.Background()) {
		t.Fatal("database error was reported as processed")
	}
}

func TestPollInterval(t *testing.T) {
	t.Setenv("POLL_INTERVAL", "250ms")
	if got := pollInterval(); got != 250*time.Millisecond {
		t.Fatalf("pollInterval() = %s, want 250ms", got)
	}
	t.Setenv("POLL_INTERVAL", "invalid")
	if got := pollInterval(); got != time.Second {
		t.Fatalf("invalid pollInterval() = %s, want 1s", got)
	}
}

func TestDatabaseURLFromPGEnvironment(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("PGHOST", "postgres-rw.shop.svc")
	t.Setenv("PGPORT", "5433")
	t.Setenv("PGDATABASE", "shop")
	t.Setenv("PGUSER", "worker")
	t.Setenv("PGPASSWORD", "p@ss/word")
	t.Setenv("PGSSLMODE", "require")

	got := databaseURL()
	want := "postgres://worker:p%40ss%2Fword@postgres-rw.shop.svc:5433/shop?sslmode=require"
	if got != want {
		t.Fatalf("databaseURL() = %q, want %q", got, want)
	}
}
