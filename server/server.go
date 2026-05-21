package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cnak-us/artifact-gateway/config"
)

// Server owns the two HTTP listeners — public (OCI + admin) and management
// (health + metrics). Both are started by ListenAndServe; both are drained
// with a 2-second timeout when the supplied context is cancelled.
//
// The public listener serves HTTPS when both TLSCertFile and TLSKeyFile are
// set (mkcert for dev, Kubernetes TLS Secret mount for self-served clusters);
// otherwise it serves plain HTTP (the production default — TLS is terminated
// at the LB / Cloudflare). The management listener is always HTTP.
type Server struct {
	Public      *http.Server
	Mgmt        *http.Server
	TLSCertFile string
	TLSKeyFile  string
	Logger      *slog.Logger
}

// New builds a Server from the configured ports. The handlers are supplied by
// the caller (typically from main.go after wiring the OCI + admin routers and
// the metrics/health handlers).
func New(cfg *config.Config, publicHandler, mgmtHandler http.Handler, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		Public: &http.Server{
			Addr:              fmt.Sprintf(":%d", cfg.PublicPort),
			Handler:           publicHandler,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       90 * time.Second,
		},
		Mgmt: &http.Server{
			Addr:              fmt.Sprintf(":%d", cfg.ManagementPort),
			Handler:           mgmtHandler,
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       30 * time.Second,
		},
		TLSCertFile: cfg.TLSCertFile,
		TLSKeyFile:  cfg.TLSKeyFile,
		Logger:      logger,
	}
}

// ListenAndServe starts both listeners. It returns when either one exits
// abnormally or when ctx is cancelled. On ctx.Done it shuts both down with a
// 2s timeout.
func (s *Server) ListenAndServe(ctx context.Context) error {
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	tlsEnabled := s.TLSCertFile != "" && s.TLSKeyFile != ""
	go func() {
		defer wg.Done()
		s.Logger.Info("public listener starting", "addr", s.Public.Addr, "tls", tlsEnabled)
		var err error
		if tlsEnabled {
			err = s.Public.ListenAndServeTLS(s.TLSCertFile, s.TLSKeyFile)
		} else {
			err = s.Public.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("public: %w", err)
		}
	}()
	go func() {
		defer wg.Done()
		s.Logger.Info("management listener starting", "addr", s.Mgmt.Addr)
		if err := s.Mgmt.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("management: %w", err)
		}
	}()

	// First-error-wins: return when ctx fires OR a server dies.
	select {
	case <-ctx.Done():
		s.Logger.Info("shutdown requested, draining")
	case err := <-errCh:
		s.Logger.Error("listener exited", "err", err)
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var shutdownErr error
	if err := s.Public.Shutdown(shutdownCtx); err != nil {
		shutdownErr = fmt.Errorf("public shutdown: %w", err)
	}
	if err := s.Mgmt.Shutdown(shutdownCtx); err != nil {
		if shutdownErr != nil {
			shutdownErr = fmt.Errorf("%v; mgmt shutdown: %w", shutdownErr, err)
		} else {
			shutdownErr = fmt.Errorf("mgmt shutdown: %w", err)
		}
	}
	wg.Wait()
	return shutdownErr
}
