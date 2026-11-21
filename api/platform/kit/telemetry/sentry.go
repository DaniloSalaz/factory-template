package telemetry

import (
	"context"
	"fmt"
	"time"

	"github.com/getsentry/sentry-go"
	"go.opentelemetry.io/otel/trace"

	"chaskicode.dev/saas/platform/kit/authctx"
)

// newSentry builds a Sentry hub of its own (not the SDK's global one) for the reports
// of s, with ADR 0004 applied to every event (BeforeSend). transport is for tests.
func newSentry(s Settings) (*sentry.Hub, error) {
	scrubber := NewScrubber()
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:         s.Config.SentryDSN.Reveal(),
		Release:     s.Version,
		Environment: string(s.Env),
		// Collect nothing of the user or the request; BeforeSend removes it anyway.
		DataCollection: &sentry.DataCollection{
			UserInfo: sentry.Set(false),
			Cookies:  &sentry.KeyValueCollectionBehavior{Mode: sentry.CollectionOff},
			HTTPHeaders: &sentry.HeaderCollectionConfig{
				Request:  &sentry.KeyValueCollectionBehavior{Mode: sentry.CollectionOff},
				Response: &sentry.KeyValueCollectionBehavior{Mode: sentry.CollectionOff},
			},
			HTTPBodies:  []sentry.BodyType{},
			QueryParams: &sentry.KeyValueCollectionBehavior{Mode: sentry.CollectionOff},
		},
		AttachStacktrace: true,
		BeforeSend: func(e *sentry.Event, _ *sentry.EventHint) *sentry.Event {
			return scrubber.ScrubEvent(e)
		},
		Transport: s.SentryTransport,
	})
	if err != nil {
		return nil, fmt.Errorf("sentry client: %w", err)
	}
	return sentry.NewHub(client, sentry.NewScope()), nil
}

// ReportError sends err to Sentry with the tags ADR 0004 allows: the request's tenant,
// route and trace. httpx.WriteProblem calls it for every 5xx, recovered panics included;
// expected 4xx never reach it. Without a DSN it does nothing.
func (t *Telemetry) ReportError(ctx context.Context, err error) {
	if t == nil || t.sentry == nil || err == nil {
		return
	}
	hub := t.sentry.Clone()
	hub.ConfigureScope(func(scope *sentry.Scope) {
		if p, ok := authctx.PrincipalFrom(ctx); ok {
			scope.SetTag(TagTenantID, p.TenantID())
		}
		if route := routeFromLabeler(ctx); route != "" {
			scope.SetTag(TagRoute, route)
		}
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			scope.SetTag(TagTraceID, sc.TraceID().String())
		}
	})
	hub.CaptureException(err)
}

// flushSentry waits for pending reports until ctx's deadline.
func flushSentry(hub *sentry.Hub) func(context.Context) error {
	return func(ctx context.Context) error {
		timeout := 2 * time.Second
		if d, ok := ctx.Deadline(); ok {
			timeout = time.Until(d)
		}
		if !hub.Flush(timeout) {
			return fmt.Errorf("sentry: reports still pending after %s", timeout)
		}
		return nil
	}
}
