// The burner is test infrastructure for Lab 4; the lab focuses on Kubernetes.
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
)

const mebibyte = 1024 * 1024

type config struct {
	port       int
	memoryMiB  int
	cpuWorkers int
}

type burner struct {
	memory     []byte
	iterations atomic.Uint64
}

func envPositiveInt(name string, fallback int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

func loadConfig() (config, error) {
	port, err := envPositiveInt("PORT", 8080)
	if err != nil {
		return config{}, err
	}
	if port > 65535 {
		return config{}, errors.New("PORT must be between 1 and 65535")
	}
	memoryMiB, err := envPositiveInt("MEMORY_MIB", 64)
	if err != nil {
		return config{}, err
	}
	if memoryMiB > int(^uint(0)>>1)/mebibyte {
		return config{}, errors.New("MEMORY_MIB is too large")
	}
	cpuWorkers, err := envPositiveInt("CPU_WORKERS", 1)
	if err != nil {
		return config{}, err
	}
	return config{port: port, memoryMiB: memoryMiB, cpuWorkers: cpuWorkers}, nil
}

func newBurner(memoryMiB int) *burner {
	memory := make([]byte, memoryMiB*mebibyte)
	// Touch every page so the allocation becomes resident memory rather than
	// remaining only virtual address space.
	for offset := 0; offset < len(memory); offset += 4096 {
		memory[offset] = 1
	}
	return &burner{memory: memory}
}

func (b *burner) burnCPU(ctx context.Context, workers int) {
	for worker := 0; worker < workers; worker++ {
		go func(id int) {
			seed := sha256.Sum256([]byte(strconv.Itoa(id)))
			var local uint64
			for {
				select {
				case <-ctx.Done():
					b.iterations.Add(local)
					return
				default:
					seed = sha256.Sum256(seed[:])
					local++
					if local == 4096 {
						b.iterations.Add(local)
						local = 0
					}
				}
			}
		}(worker)
	}
}

func (b *burner) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = fmt.Fprintf(w,
			"# HELP burner_memory_bytes Bytes retained by the burner.\n"+
				"# TYPE burner_memory_bytes gauge\n"+
				"burner_memory_bytes %d\n"+
				"# HELP burner_cpu_iterations_total Completed CPU burn iterations.\n"+
				"# TYPE burner_cpu_iterations_total counter\n"+
				"burner_cpu_iterations_total %d\n",
			len(b.memory), b.iterations.Load(),
		)
	})
	return mux
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	load := newBurner(cfg.memoryMiB)
	load.burnCPU(ctx, cfg.cpuWorkers)

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.port),
		Handler:           load.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	logger.Info("burner started",
		"port", cfg.port,
		"memory_mib", cfg.memoryMiB,
		"cpu_workers", cfg.cpuWorkers,
	)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("http server failed", "error", err)
		os.Exit(1)
	}
	runtime.KeepAlive(load)
}
