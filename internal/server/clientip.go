package server

import (
	"net"
	"net/http"
	"net/netip"

	"github.com/go-chi/chi/v5/middleware"
)

// clientIPMiddleware resolves the address a request came from and puts it in the
// request context, where the rate limiter and the audit ledger read it.
//
// The address has to come from the connection unless something portal trusts
// says otherwise. X-Forwarded-For is written by whoever is speaking to the
// server, so a caller who picks their own value picks their own rate-limit
// bucket and their own line in the audit ledger.
//
// With no trusted proxies configured the peer address is the client address and
// forwarded headers are ignored. That is the right default for a template: it is
// wrong only in the direction of attributing traffic to a proxy, which is
// visible immediately, whereas trusting a header by default is wrong in the
// direction of believing whatever it says, which is not visible at all.
//
// Given trusted CIDRs, chi walks X-Forwarded-For right-to-left and takes the
// first entry that is not one of them — the last address a trusted hop actually
// observed. Everything left of that was written before the request reached
// anything portal trusts.
func clientIPMiddleware(trustedProxyCIDRs []string) func(http.Handler) http.Handler {
	if len(trustedProxyCIDRs) == 0 {
		return func(h http.Handler) http.Handler { return middleware.ClientIPFromRemoteAddr(h) }
	}
	return middleware.ClientIPFromXFF(trustedProxyCIDRs...)
}

// clientIPOf returns the resolved client IP, falling back to the connection's
// own address.
//
// The fallback is reached when the resolved address is absent, which chi does
// deliberately: an unparseable X-Forwarded-For entry aborts its walk rather than
// trusting what is left of the garbage. Falling back to the peer keeps a
// rate-limit key and an audit entry for that request instead of grouping every
// malformed request under one empty key.
func clientIPOf(r *http.Request) string {
	if ip := middleware.GetClientIP(r.Context()); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.Unmap().String()
	}
	return host
}
