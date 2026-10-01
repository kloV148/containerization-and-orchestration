package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("MEMORY_MIB", "8")
	t.Setenv("CPU_WORKERS", "2")

	got, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.port != 9090 || got.memoryMiB != 8 || got.cpuWorkers != 2 {
		t.Fatalf("config = %+v", got)
	}
}

func TestLoadConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "PORT", value: "65536"},
		{name: "MEMORY_MIB", value: "0"},
		{name: "CPU_WORKERS", value: "many"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PORT", "8080")
			t.Setenv("MEMORY_MIB", "1")
			t.Setenv("CPU_WORKERS", "1")
			t.Setenv(tt.name, tt.value)
			if _, err := loadConfig(); err == nil {
				t.Fatalf("loadConfig accepted %s=%q", tt.name, tt.value)
			}
		})
	}
}

func TestNewBurnerAllocatesRequestedMemory(t *testing.T) {
	b := newBurner(2)
	if got, want := len(b.memory), 2*mebibyte; got != want {
		t.Fatalf("memory length = %d, want %d", got, want)
	}
	for offset := 0; offset < len(b.memory); offset += 4096 {
		if b.memory[offset] != 1 {
			t.Fatalf("page at offset %d was not touched", offset)
		}
	}
}

func TestHandler(t *testing.T) {
	b := newBurner(1)

	health := httptest.NewRecorder()
	b.handler().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK || health.Body.String() != "ok\n" {
		t.Fatalf("health response = (%d, %q)", health.Code, health.Body.String())
	}

	metrics := httptest.NewRecorder()
	b.handler().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "burner_memory_bytes 1048576") {
		t.Fatalf("memory metric is missing: %s", metrics.Body.String())
	}
	if !strings.Contains(metrics.Body.String(), "burner_cpu_iterations_total 0") {
		t.Fatalf("CPU metric is missing: %s", metrics.Body.String())
	}
}
