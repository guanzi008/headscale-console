package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

type httpService struct {
	name    string
	server  *http.Server
	errc    chan error
	address string
}

func startHTTP(name, address string, handler http.Handler, certFile, keyFile string, logger *slog.Logger) (*httpService, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen for %s on %s: %w", name, address, err)
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	service := &httpService{
		name:    name,
		server:  server,
		errc:    make(chan error, 1),
		address: address,
	}

	tlsEnabled := certFile != "" || keyFile != ""
	if tlsEnabled {
		certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			_ = listener.Close()
			return nil, fmt.Errorf("load TLS certificate for %s: %w", name, err)
		}
		server.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{certificate},
			MinVersion:   tls.VersionTLS12,
		}
	}

	go func() {
		logger.Info("HTTP service listening", "service", name, "address", address, "tls", tlsEnabled)
		var serveErr error
		if tlsEnabled {
			serveErr = server.ServeTLS(listener, "", "")
		} else {
			serveErr = server.Serve(listener)
		}
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		service.errc <- serveErr
	}()
	return service, nil
}

func (s *httpService) Wait() error {
	return <-s.errc
}

func (s *httpService) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

func waitForHTTP(ctx context.Context, client *http.Client, endpoint string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var lastStatus string
	var lastError error
	for {
		request, err := http.NewRequestWithContext(waitCtx, http.MethodGet, endpoint, nil)
		if err != nil {
			return fmt.Errorf("build readiness request: %w", err)
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil
			}
			lastStatus = response.Status
		} else {
			lastError = err
		}

		select {
		case <-waitCtx.Done():
			if lastStatus != "" {
				return fmt.Errorf("wait for %s: last status %s", endpoint, lastStatus)
			}
			if lastError != nil {
				return fmt.Errorf("wait for %s: %w", endpoint, lastError)
			}
			return fmt.Errorf("wait for %s: %w", endpoint, waitCtx.Err())
		case <-ticker.C:
		}
	}
}
