//go:build bundled_nginx || bundled_angie || bundled_caddy || bundled_haproxy || bundled_traefik

package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

func runWithBundledRedirect(ctx context.Context, run func(context.Context) error) error {
	// Traefik's redirect middleware sends `301`; bundles promise `308`.
	listener, err := net.Listen("tcp", "127.0.0.1:8082")
	if err != nil {
		return err
	}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if hostname, _, err := net.SplitHostPort(host); err == nil {
			host = hostname
		}
		w.Header().Set("Location", "https://"+host+r.URL.RequestURI())
		w.WriteHeader(http.StatusPermanentRedirect)
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	err = run(ctx)
	_ = server.Close()
	if serveErr := <-done; serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		err = errors.Join(err, serveErr)
	}
	return err
}
