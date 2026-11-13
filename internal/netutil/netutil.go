// Package netutil holds small network helpers shared by the server and
// its packages.
package netutil

import (
	"net"
	"strings"
)

// LoopbackHost reports whether host (a name or IP, without a port) is this
// machine: localhost in any case, or a loopback address.
func LoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
