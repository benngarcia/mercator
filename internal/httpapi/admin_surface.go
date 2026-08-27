package httpapi

import (
	"net"
	"net/http"
)

// administrativePatterns are the operations that change which machines
// Mercator may use or force event delivery. None belongs on the address ordinary
// API traffic arrives at. They are patterns rather than prefixes because the
// reads beside them are not administrative.
var administrativePatterns = []string{
	"POST /v1/nodes",
	"POST /v1/sinks/{sink_id}/deliver",
	"POST /v1/sinks/{sink_id}/replay",
}

// adminSurface knows which requests are administrative and which listener may
// answer them. It matches with a ServeMux so the patterns above are read by the
// same router that routes them, rather than by a second string comparison that
// could drift from it.
type adminSurface struct {
	addr     string
	patterns *http.ServeMux
}

func newAdminSurface(addr string) *adminSurface {
	patterns := http.NewServeMux()
	administrative := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for _, pattern := range administrativePatterns {
		patterns.Handle(pattern, administrative)
	}
	return &adminSurface{addr: addr, patterns: patterns}
}

// WithAdminAddr names the local address of the listener the administrative
// operations answer on. The address must be the one a listener is actually
// bound to, wildcards resolved, because it is compared against the local
// address of each accepted connection.
//
// Without it every route answers on every listener, which is what a loopback
// deployment with one listener wants. The process entrypoint is what refuses to
// start a non-loopback deployment that has not set one, because only it knows
// the public address.
func WithAdminAddr(addr string) Option {
	return func(s *Server) { s.admin = newAdminSurface(addr) }
}

// unroutedHere reports that this request names an administrative operation and
// did not arrive on the administrative listener, so this listener does not
// serve it and answers as it does for a path this deployment does not have.
func (a *adminSurface) unroutedHere(r *http.Request) bool {
	return a.covers(r) && !a.servedAdministratively(r)
}

func (a *adminSurface) covers(r *http.Request) bool {
	_, pattern := a.patterns.Handler(r)
	return pattern != ""
}

// servedAdministratively reports whether the request arrived on the
// administrative listener. net/http records the accepting listener's address on
// every request context, so this is the connection's own answer rather than a
// header a caller could set.
func (a *adminSurface) servedAdministratively(r *http.Request) bool {
	local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	return ok && local.String() == a.addr
}
