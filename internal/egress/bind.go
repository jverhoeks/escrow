package egress

import "net"

// ExposedBind reports whether binding to host exposes the proxy beyond loopback
// (e.g. 0.0.0.0, ::, or a routable IP). Loopback / localhost / empty => false.
func ExposedBind(host string) bool {
	if host == "" || host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil { // a hostname we can't classify — be conservative, treat as not exposed
		return false
	}
	return !ip.IsLoopback()
}
