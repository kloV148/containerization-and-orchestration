// Code generated with AI solely for the purposes of Lab 1.
// The service is test infrastructure; application development is not the lab goal.
package main

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
)

var (
	allocationsMu sync.Mutex
	allocations   [][]byte
	burnOnce      sync.Once
	burnCounter   uint64
)

func health(w http.ResponseWriter, _ *http.Request) {
	_, _ = fmt.Fprintln(w, "ok")
}

func eat(w http.ResponseWriter, r *http.Request) {
	mb, err := strconv.Atoi(r.URL.Query().Get("mb"))
	if err != nil || mb <= 0 {
		http.Error(w, "use /eat?mb=N, where N is a positive integer", http.StatusBadRequest)
		return
	}

	memory := make([]byte, mb*1024*1024)
	for i := 0; i < len(memory); i += 4096 {
		memory[i] = 1 // Touch every page so the allocation becomes resident memory.
	}

	allocationsMu.Lock()
	allocations = append(allocations, memory) // Keep it reachable and therefore allocated.
	total := 0
	for _, allocation := range allocations {
		total += len(allocation)
	}
	allocationsMu.Unlock()

	_, _ = fmt.Fprintf(w, "holding %d MB\n", total/(1024*1024))
}

func burn(w http.ResponseWriter, _ *http.Request) {
	burnOnce.Do(func() {
		go func() {
			for {
				atomic.AddUint64(&burnCounter, 1)
			}
		}()
	})

	w.WriteHeader(http.StatusAccepted)
	_, _ = fmt.Fprintln(w, "burning one CPU core")
}

func main() {
	http.HandleFunc("/health", health)
	http.HandleFunc("/eat", eat)
	http.HandleFunc("/burn", burn)

	log.Println("api listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
