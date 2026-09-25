package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type memoryStore struct {
	orders    []order
	createErr error
	listErr   error
}

func (s *memoryStore) CreateOrder(_ context.Context, input createOrderInput) (order, error) {
	if s.createErr != nil {
		return order{}, s.createErr
	}
	created := order{
		ID:        int64(len(s.orders) + 1),
		Item:      input.Item,
		Quantity:  input.Quantity,
		CreatedAt: time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC),
	}
	s.orders = append(s.orders, created)
	return created, nil
}

func (s *memoryStore) ListOrders(context.Context) ([]order, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.orders, nil
}

func testHandler(store orderStore, healthFail bool) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	_, handler := newApp(logger, store, healthFail)
	return handler
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
			testHandler(&memoryStore{}, tt.healthFail).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
			if response.Code != tt.wantStatus || response.Body.String() != tt.wantBody {
				t.Fatalf("response = (%d, %q), want (%d, %q)", response.Code, response.Body.String(), tt.wantStatus, tt.wantBody)
			}
		})
	}
}

func TestCreateAndListOrders(t *testing.T) {
	store := &memoryStore{}
	handler := testHandler(store, false)

	createdResponse := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/order", strings.NewReader(`{"item":"keyboard","quantity":2}`))
	handler.ServeHTTP(createdResponse, request)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createdResponse.Code, createdResponse.Body)
	}
	var created order
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID != 1 || created.Item != "keyboard" || created.Quantity != 2 || created.Processed {
		t.Fatalf("created order = %+v", created)
	}

	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, httptest.NewRequest(http.MethodGet, "/orders", nil))
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listResponse.Code, listResponse.Body)
	}
	var orders []order
	if err := json.NewDecoder(listResponse.Body).Decode(&orders); err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 || orders[0].ID != created.ID {
		t.Fatalf("orders = %+v", orders)
	}
}

func TestCreateOrderValidation(t *testing.T) {
	tests := []string{
		`{}`,
		`{"item":"keyboard","quantity":0}`,
		`{"item":"keyboard","quantity":1,"unknown":true}`,
		`not-json`,
		`{"item":"keyboard","quantity":1} {}`,
	}
	for _, body := range tests {
		response := httptest.NewRecorder()
		testHandler(&memoryStore{}, false).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/order", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, response.Code)
		}
	}
}

func TestDatabaseErrorsReturnServiceUnavailable(t *testing.T) {
	store := &memoryStore{createErr: errors.New("down"), listErr: errors.New("down")}
	handler := testHandler(store, false)

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/order", bytes.NewBufferString(`{"item":"keyboard","quantity":1}`)),
		httptest.NewRequest(http.MethodGet, "/orders", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status = %d, want 503", request.Method, request.URL.Path, response.Code)
		}
	}
}

func TestMetrics(t *testing.T) {
	handler := testHandler(&memoryStore{}, false)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))
	metrics := httptest.NewRecorder()
	handler.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), `shop_http_requests_total{code="200",method="GET",path="/health"} 1`) {
		t.Fatalf("health request metric is missing: %s", metrics.Body.String())
	}
}

func TestDatabaseURLFromPGEnvironment(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("PGHOST", "postgres-rw.shop.svc")
	t.Setenv("PGPORT", "5433")
	t.Setenv("PGDATABASE", "shop")
	t.Setenv("PGUSER", "api")
	t.Setenv("PGPASSWORD", "p@ss/word")
	t.Setenv("PGSSLMODE", "require")

	got := databaseURL()
	want := "postgres://api:p%40ss%2Fword@postgres-rw.shop.svc:5433/shop?sslmode=require"
	if got != want {
		t.Fatalf("databaseURL() = %q, want %q", got, want)
	}
}
