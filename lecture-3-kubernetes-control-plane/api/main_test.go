package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	_, handler := newApp(logger, store, appConfig{version: "v1", healthFail: healthFail})
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
	if !strings.Contains(metrics.Body.String(), `http_requests_total{code="200",method="GET",path="/health"} 1`) {
		t.Fatalf("health request metric is missing: %s", metrics.Body.String())
	}
	if !strings.Contains(metrics.Body.String(), `http_request_duration_seconds_count{code="200",method="GET",path="/health"} 1`) {
		t.Fatalf("health duration metric is missing: %s", metrics.Body.String())
	}

	failingHandler := testHandler(&memoryStore{}, true)
	failingHandler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))
	failingMetrics := httptest.NewRecorder()
	failingHandler.ServeHTTP(failingMetrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(failingMetrics.Body.String(), `http_errors_total{code="503",method="GET",path="/health"} 1`) {
		t.Fatalf("health error metric is missing: %s", failingMetrics.Body.String())
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

func TestReleaseModes(t *testing.T) {
	for _, tt := range []struct {
		name     string
		config   appConfig
		samples  []float64
		statuses []int
	}{
		{"v1 ignores FAIL", appConfig{version: "v1", fail: true}, []float64{0}, []int{201}},
		{"v2 ignores FLAKY", appConfig{version: "v2", flaky: true}, []float64{0}, []int{201}},
		{"bad v2", appConfig{version: "v2", fail: true}, []float64{0, 0.9}, []int{500, 500}},
		{"flaky v1", appConfig{version: "v1", flaky: true}, []float64{0.1, 0.2, 0.9, 0}, []int{500, 201, 201, 500}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &memoryStore{}
			a, handler := newApp(slog.New(slog.NewJSONHandler(io.Discard, nil)), store, tt.config)
			version := httptest.NewRecorder()
			handler.ServeHTTP(version, httptest.NewRequest("GET", "/version", nil))
			if version.Code != 200 || version.Body.String() != tt.config.version+"\n" {
				t.Fatalf("version: %d %q", version.Code, version.Body.String())
			}
			created, failed := 0, 0
			for i, sample := range tt.samples {
				a.randomFloat = func() float64 { return sample }
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest("POST", "/order", strings.NewReader(`{"item":"keyboard","quantity":1}`)))
				if response.Code != tt.statuses[i] {
					t.Fatalf("request %d: status %d, want %d", i, response.Code, tt.statuses[i])
				}
				if response.Code == 201 {
					created++
				} else {
					failed++
				}
			}
			if len(store.orders) != created {
				t.Fatalf("stored %d orders, want %d", len(store.orders), created)
			}
			for _, path := range []string{"/health", "/orders"} {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
				if response.Code != 200 {
					t.Fatalf("%s: status %d", path, response.Code)
				}
			}
			metrics := httptest.NewRecorder()
			handler.ServeHTTP(metrics, httptest.NewRequest("GET", "/metrics", nil))
			if failed > 0 {
				for _, metric := range []string{"http_requests_total", "http_errors_total", "http_request_duration_seconds_count"} {
					want := fmt.Sprintf(`%s{code="500",method="POST",path="/order"} %d`, metric, failed)
					if !strings.Contains(metrics.Body.String(), want) {
						t.Fatalf("missing metric %s", want)
					}
				}
			}
		})
	}
}

func TestReleaseConfiguration(t *testing.T) {
	t.Setenv("VERSION", "")
	t.Setenv("FAIL", "false")
	t.Setenv("FLAKY", "false")
	t.Setenv("HEALTH_FAIL", "false")
	config, err := configFromEnv()
	if err != nil || config.version != "v1" || config.fail || config.flaky {
		t.Fatalf("default configuration: %+v, %v", config, err)
	}
	t.Setenv("VERSION", "v2")
	t.Setenv("FAIL", "true")
	t.Setenv("FLAKY", "true")
	config, err = configFromEnv()
	if err != nil || config.version != "v2" || !config.fail || !config.flaky {
		t.Fatalf("configuration: %+v, %v", config, err)
	}
	t.Setenv("VERSION", "v3")
	if _, err := configFromEnv(); err == nil {
		t.Fatal("invalid VERSION accepted")
	}
}
