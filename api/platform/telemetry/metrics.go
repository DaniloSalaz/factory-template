package telemetry

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
)

// Metrics are the API's own counters. Only authz.denied carries tenant_id (ADR 0004).
// A nil *Metrics records nothing, so use cases and tests may go without one.
type Metrics struct {
	login, mfaVerify, authzDenied, tenantForbidden, rateLimited, invitationEmail metric.Int64Counter
}

// Results of the counters that have one: a closed vocabulary, never free text.
const (
	ResultSuccess = "success"
	ResultFailure = "failure"
)

func newMetrics(mp metric.MeterProvider) (*Metrics, error) {
	m := mp.Meter("github.com/DaniloSalaz/factory-template/api")
	var errs []error
	counter := func(name, desc string) metric.Int64Counter {
		c, err := m.Int64Counter(name, metric.WithDescription(desc))
		errs = append(errs, err)
		return c
	}
	out := &Metrics{
		login:           counter("auth.login", "Logins completed or refused, by result"),
		mfaVerify:       counter("mfa.verify", "MFA code verifications, by result"),
		authzDenied:     counter("authz.denied", "Permissions refused by the use cases, by tenant"),
		tenantForbidden: counter("auth.tenant_forbidden", "Requests refused for the tenant they named; no tenant_id: the id is the prober's"),
		rateLimited:     counter("rate_limit.exceeded", "Requests refused by a quota (429), by route"),
		invitationEmail: counter("invitation.email", "Invitation emails sent or failed, by result"),
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("metrics: %w", err)
	}
	return out, nil
}

// Login counts a completed login (result success) or a refused one (its reason, such
// as stale_auth or email_unverified).
func (m *Metrics) Login(ctx context.Context, result string) {
	if m != nil {
		m.login.Add(ctx, 1, metric.WithAttributes(KeyResult.String(result)))
	}
}

// MFAVerify counts a verification: success, or the reason it failed.
func (m *Metrics) MFAVerify(ctx context.Context, result string) {
	if m != nil {
		m.mfaVerify.Add(ctx, 1, metric.WithAttributes(KeyResult.String(result)))
	}
}

// AuthzDenied counts a refused permission in tenantID.
func (m *Metrics) AuthzDenied(ctx context.Context, tenantID string) {
	if m != nil {
		m.authzDenied.Add(ctx, 1, metric.WithAttributes(KeyTenantID.String(tenantID)))
	}
}

// TenantForbidden counts a request refused for its X-Tenant-ID. It takes no tenant on
// purpose: the requested id is chosen by whoever probes for other tenants.
func (m *Metrics) TenantForbidden(ctx context.Context) {
	if m != nil {
		m.tenantForbidden.Add(ctx, 1)
	}
}

// RateLimited counts a 429 on route (the ServeMux pattern's path, "" when unrouted).
func (m *Metrics) RateLimited(ctx context.Context, route string) {
	if m != nil {
		m.rateLimited.Add(ctx, 1, metric.WithAttributes(semconv.HTTPRoute(route)))
	}
}

// InvitationEmail counts an invitation email: ResultSuccess or ResultFailure.
func (m *Metrics) InvitationEmail(ctx context.Context, result string) {
	if m != nil {
		m.invitationEmail.Add(ctx, 1, metric.WithAttributes(KeyResult.String(result)))
	}
}

// routeAttr returns the http.route of a ServeMux pattern: its path, without the method.
func routeAttr(pattern string) attribute.KeyValue {
	return semconv.HTTPRoute(RouteOf(pattern))
}
