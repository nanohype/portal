package handler

import (
	"net"
	"net/http"
	"net/netip"

	"github.com/go-chi/chi/v5/middleware"
)

// auditContext is the caller identity an audit entry records alongside the
// change: where the request came from, and what it said it was.
//
// The address is the one the client-IP middleware resolved, which is the
// connection's own unless a proxy portal trusts is in front of it. Reading
// X-Forwarded-For here instead would let the caller write their own line in the
// ledger — the record of who did something is worth less than nothing if the
// person doing it chooses it.
//
// User-Agent is what the caller says it is and is recorded as such; nothing
// depends on it.
func auditContext(r *http.Request) (ip, userAgent string) {
	userAgent = r.Header.Get("User-Agent")

	if resolved := middleware.GetClientIP(r.Context()); resolved != "" {
		return resolved, userAgent
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
		return addr.Unmap().String(), userAgent
	}
	return host, userAgent
}
