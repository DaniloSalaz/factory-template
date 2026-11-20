// Package domain holds the health module's business concepts. It must not import infrastructure.
package domain

// Readiness tells whether the service can take traffic and, if not, why.
type Readiness struct {
	ready  bool
	reason string
	cause  error
}

// Ready reports a service able to take traffic.
func Ready() Readiness {
	return Readiness{ready: true}
}

// NotReady reports a service that cannot take traffic; reason is safe to expose, cause is not.
func NotReady(reason string, cause error) Readiness {
	return Readiness{reason: reason, cause: cause}
}

// IsReady reports whether the service can take traffic.
func (r Readiness) IsReady() bool { return r.ready }

// Reason is a client-safe explanation of why the service is not ready.
func (r Readiness) Reason() string { return r.reason }

// Cause is the underlying error, meant for logs only.
func (r Readiness) Cause() error { return r.cause }
