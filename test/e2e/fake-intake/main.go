// A stand-in for benchhud-intake, for the end-to-end test only. It writes
// down what the relay sends and says yes. It checks nothing but the token:
// the relay's own tests hold it to intake's contract, and test/e2e.sh does
// the asserting here.
//
//	POST /api/requests   records the request, answers 202 (401 on a bad token)
//	GET  /received       everything recorded so far, as a JSON list
//	GET  /healthz        ok
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
)

type received struct {
	Authorization  string          `json:"authorization"`
	IdempotencyKey string          `json:"idempotency_key"`
	ContentType    string          `json:"content_type"`
	Body           json.RawMessage `json:"body"`
}

func main() {
	token := os.Getenv("SUBMIT_TOKEN")
	if token == "" {
		log.Fatal("SUBMIT_TOKEN is not set")
	}

	var mu sync.Mutex
	seen := []received{}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/requests", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintln(w, `{"error":"unauthorized"}`)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil || !json.Valid(body) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintln(w, `{"errors":["not JSON"]}`)
			return
		}
		mu.Lock()
		seen = append(seen, received{
			Authorization:  r.Header.Get("Authorization"),
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
			ContentType:    r.Header.Get("Content-Type"),
			Body:           body,
		})
		n := len(seen)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, "{\"id\":\"fake-%d\"}\n", n)
	})
	mux.HandleFunc("GET /received", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(seen)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	log.Fatal(http.ListenAndServe(":8000", mux))
}
