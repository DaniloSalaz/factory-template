// Package http may leave exported identifiers undocumented: their contract is the port's.
package http

type Handler struct{}

func (*Handler) Serve() {}
