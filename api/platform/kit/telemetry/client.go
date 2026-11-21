package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"chaskicode.dev/saas/platform/apperr"
)

// ResultOf names an outcome for a metric or a span: success, the code of an apperr
// (mfa.code_invalid, auth.stale_authentication…, a closed vocabulary) or internal.
func ResultOf(err error) string {
	if err == nil {
		return ResultSuccess
	}
	if e, ok := apperr.As(err); ok && e.Code != "" {
		return e.Code
	}
	return "internal"
}

// TenantEntered records the request's tenant on its span (ADR 0004 allows tenant_id on
// traces, never on HTTP metrics).
func TenantEntered(ctx context.Context, tenantID string) {
	trace.SpanFromContext(ctx).SetAttributes(KeyTenantID.String(tenantID))
}

// ClientSpan starts a client span for a call to an external service (peer.service:
// workos, smtp) named "<service>.<operation>", with no URL, parameters or body. The
// returned function ends it with the call's result.
func ClientSpan(ctx context.Context, tp trace.TracerProvider, service, operation string) (context.Context, func(error)) {
	if tp == nil {
		tp = tracenoop.NewTracerProvider()
	}
	ctx, span := tp.Tracer("chaskicode.dev/saas").Start(ctx, service+"."+operation,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(KeyPeerService.String(service)))
	return ctx, func(err error) {
		result := ResultOf(err)
		if err != nil && result == "internal" {
			result = ResultFailure
		}
		span.SetAttributes(KeyResult.String(result))
		if err != nil {
			// The description is the result, never the error's text.
			span.SetStatus(codes.Error, result)
		}
		span.End()
	}
}
