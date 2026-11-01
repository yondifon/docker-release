// Command bluegreen-app is the app behind tests/bundled/check-bluegreen.py.
// Set MODE=bad and `/signup` answers 500; every other path answers 200 and
// says which version and container served it.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	mode := os.Getenv("MODE")
	host, _ := os.Hostname()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if mode == "bad" && strings.HasPrefix(r.URL.Path, "/signup") {
			http.Error(w, "version=bad signup is broken", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "version=%s host=%s path=%s\n", mode, host, r.URL.Path)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })

	server := &http.Server{Addr: ":80", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
