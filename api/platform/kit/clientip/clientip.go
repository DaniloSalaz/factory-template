// Package clientip decides which address a request comes from and is the only place
// that truncates it: for storage (auth-y-tenancy §6), /24 for IPv4 and /48 for IPv6;
// for rate limiting, /32 and /64.
package clientip

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

const (
	ipv4PrefixBits = 24
	ipv6PrefixBits = 48

	ipv4LimitBits = 32
	// A /64 is the smallest IPv6 block a client normally gets, and it holds 2^64
	// addresses: limiting by the full address would let one client rotate around it.
	ipv6LimitBits = 64
)

// Resolver reads the client address of a request. With a trusted header (CF-Connecting-IP
// behind Cloudflare Tunnel) it reads only that header; without one, only RemoteAddr.
// X-Forwarded-For is never read: the client controls it.
type Resolver struct {
	header string
}

// NewResolver builds a Resolver. An empty header means RemoteAddr.
func NewResolver(trustedHeader string) Resolver {
	return Resolver{header: trustedHeader}
}

// Addr returns the client address, or false if it is missing or malformed. A request
// that lacks the trusted header did not come through the proxy that sets it, so it has
// no known address rather than falling back to RemoteAddr.
func (r Resolver) Addr(req *http.Request) (netip.Addr, bool) {
	if r.header != "" {
		return parse(strings.TrimSpace(req.Header.Get(r.header)))
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	return parse(host)
}

func parse(s string) (netip.Addr, bool) {
	if s == "" {
		return netip.Addr{}, false
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.WithZone("").Unmap(), true
}

// Prefix truncates addr to the prefix that may be stored: /24 for IPv4 (IPv4-mapped IPv6
// included) and /48 for IPv6.
func Prefix(addr netip.Addr) (netip.Prefix, bool) {
	return truncate(addr, ipv4PrefixBits, ipv6PrefixBits)
}

// LimitKey returns the block a rate limit counts by: the address itself for IPv4 (/32)
// and its /64 for IPv6.
func LimitKey(addr netip.Addr) (netip.Prefix, bool) {
	return truncate(addr, ipv4LimitBits, ipv6LimitBits)
}

func truncate(addr netip.Addr, v4Bits, v6Bits int) (netip.Prefix, bool) {
	if !addr.IsValid() {
		return netip.Prefix{}, false
	}
	addr = addr.WithZone("").Unmap()
	bits := v6Bits
	if addr.Is4() {
		bits = v4Bits
	}
	p, err := addr.Prefix(bits)
	if err != nil {
		return netip.Prefix{}, false
	}
	return p, true
}

type ctxKey struct{}

// WithAddr returns a context carrying the client address.
func WithAddr(ctx context.Context, addr netip.Addr) context.Context {
	return context.WithValue(ctx, ctxKey{}, addr)
}

// AddrFrom returns the client address stored in ctx, if any.
func AddrFrom(ctx context.Context) (netip.Addr, bool) {
	addr, ok := ctx.Value(ctxKey{}).(netip.Addr)
	return addr, ok && addr.IsValid()
}

// PrefixFrom returns the truncated client address stored in ctx, if any.
func PrefixFrom(ctx context.Context) (netip.Prefix, bool) {
	addr, ok := AddrFrom(ctx)
	if !ok {
		return netip.Prefix{}, false
	}
	return Prefix(addr)
}
