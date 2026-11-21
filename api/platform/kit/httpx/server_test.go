package httpx

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"chaskicode.dev/saas/platform/kit/config"
)

func testHTTPConfig(shutdown time.Duration) config.HTTP {
	return config.HTTP{
		Port:              0,
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       5 * time.Second,
		ShutdownTimeout:   shutdown,
		MaxHeaderBytes:    4096,
	}
}

func listenLocal(t *testing.T) net.Listener {
	t.Helper()

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln
}

func TestNewServer_AppliesConfig(t *testing.T) {
	t.Parallel()

	cfg := testHTTPConfig(3 * time.Second)
	cfg.Port = 9123
	s := NewServer(cfg, http.NotFoundHandler(), slog.New(slog.DiscardHandler))

	tests := []struct {
		name      string
		got, want any
	}{
		{"addr", s.srv.Addr, ":9123"},
		{"read header timeout", s.srv.ReadHeaderTimeout, cfg.ReadHeaderTimeout},
		{"read timeout", s.srv.ReadTimeout, cfg.ReadTimeout},
		{"write timeout", s.srv.WriteTimeout, cfg.WriteTimeout},
		{"idle timeout", s.srv.IdleTimeout, cfg.IdleTimeout},
		{"max header bytes", s.srv.MaxHeaderBytes, cfg.MaxHeaderBytes},
		{"shutdown timeout", s.shutdownTimeout, cfg.ShutdownTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.got != tc.want {
				t.Fatalf("got %v, want %v", tc.got, tc.want)
			}
		})
	}
}

func TestServer_Shutdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		shutdownTimeout time.Duration
		releaseAfter    bool // release the slow handler after cancelling
		wantErr         error
	}{
		{"in-flight request finishes before shutdown completes", 5 * time.Second, true, nil},
		{"drain that exceeds the timeout is an error", 50 * time.Millisecond, false, context.DeadlineExceeded},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			started := make(chan struct{})
			release := make(chan struct{})
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				close(started)
				<-release
				_, _ = io.WriteString(w, "done")
			})

			ln := listenLocal(t)
			s := NewServer(testHTTPConfig(tc.shutdownTimeout), h, slog.New(slog.DiscardHandler))
			ctx, cancel := context.WithCancel(t.Context())
			served := make(chan error, 1)
			go func() { served <- s.serve(ctx, ln) }()

			type result struct {
				body string
				err  error
			}
			got := make(chan result, 1)
			go func() {
				req, _ := http.NewRequestWithContext(context.WithoutCancel(t.Context()), http.MethodGet, "http://"+ln.Addr().String()+"/slow", nil)
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					got <- result{err: err}
					return
				}
				defer func() { _ = res.Body.Close() }()
				b, err := io.ReadAll(res.Body)
				got <- result{body: string(b), err: err}
			}()

			<-started
			cancel()

			if !tc.releaseAfter {
				err := <-served
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("serve error = %v, want %v", err, tc.wantErr)
				}
				return
			}

			select {
			case err := <-served:
				t.Fatalf("server stopped before the in-flight request finished: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			close(release)

			r := <-got
			if r.err != nil || r.body != "done" {
				t.Fatalf("in-flight request = %q, %v; want \"done\"", r.body, r.err)
			}
			if err := <-served; err != nil {
				t.Fatalf("serve error = %v, want nil", err)
			}
		})
	}
}

func TestServer_RunFailsWhenItCannotListen(t *testing.T) {
	t.Parallel()

	// Occupy the port on all interfaces, as Run does; a loopback-only listener would not
	// conflict with ":port" on every OS.
	var lc net.ListenConfig
	busy, err := lc.Listen(t.Context(), "tcp", ":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = busy.Close() })
	_, port, err := net.SplitHostPort(busy.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}

	cfg := testHTTPConfig(time.Second)
	cfg.Port, _ = strconv.Atoi(port)
	// Bounded so a regression (Run succeeding) fails instead of hanging.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err = NewServer(cfg, http.NotFoundHandler(), slog.New(slog.DiscardHandler)).Run(ctx)
	if err == nil {
		t.Fatal("Run on a busy port returned nil")
	}
}
