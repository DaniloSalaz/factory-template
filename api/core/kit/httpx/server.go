package httpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"chaskicode.dev/saas/platform/kit/config"
)

// Server is an HTTP server with configured timeouts and graceful shutdown.
type Server struct {
	srv             *http.Server
	logger          *slog.Logger
	shutdownTimeout time.Duration
}

// NewServer builds a Server for h. It does not start listening; call Run.
func NewServer(cfg config.HTTP, h http.Handler, logger *slog.Logger) *Server {
	return &Server{
		srv: &http.Server{
			Addr:              cfg.Addr(),
			Handler:           h,
			ReadHeaderTimeout: cfg.ReadHeaderTimeout,
			ReadTimeout:       cfg.ReadTimeout,
			WriteTimeout:      cfg.WriteTimeout,
			IdleTimeout:       cfg.IdleTimeout,
			MaxHeaderBytes:    cfg.MaxHeaderBytes,
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
		},
		logger:          logger,
		shutdownTimeout: cfg.ShutdownTimeout,
	}
}

// Run listens and serves until ctx is cancelled, then stops accepting connections and
// waits for in-flight requests up to the shutdown timeout. It returns an error if the
// server cannot start, fails while serving, or does not drain in time.
func (s *Server) Run(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.srv.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.srv.Addr, err)
	}
	return s.serve(ctx, ln)
}

func (s *Server) serve(ctx context.Context, ln net.Listener) error {
	errCh := make(chan error, 1)
	go func() {
		s.logger.InfoContext(ctx, "http server listening", slog.String("addr", ln.Addr().String()))
		errCh <- s.srv.Serve(ln)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("serve http: %w", err)
	case <-ctx.Done():
	}

	s.logger.InfoContext(ctx, "shutting down http server", slog.Duration("timeout", s.shutdownTimeout))
	// ctx is already cancelled, so the drain deadline must not inherit its cancellation.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
	defer cancel()

	if err := s.srv.Shutdown(shutdownCtx); err != nil {
		closeErr := s.srv.Close()
		return fmt.Errorf("shutdown http server: %w", errors.Join(err, closeErr))
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve http: %w", err)
	}
	s.logger.InfoContext(ctx, "http server stopped")
	return nil
}
