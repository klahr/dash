package notify

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ErrPrivateAddress is returned for webhook URLs that point into the
// network Dash runs in (loopback, private, link-local and similar
// addresses) when those aren't allowed: on a hosted Dash a webhook must not
// become a way to reach its internal services.
var ErrPrivateAddress = errors.New("webhook URLs may not point to private, loopback or link-local addresses on this Dash")

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT (and tailnets)
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64 can embed any IPv4 address
}

// blockedIP reports whether ip is an address a hosted Dash must not call.
func blockedIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// CheckURL is the early check of a webhook URL when it's saved: literal
// private addresses and localhost names are refused unless allowPrivate.
// Names that resolve to such addresses are caught when connecting (the
// answer may change between now and then).
func CheckURL(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if allowPrivate {
		return nil
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return ErrPrivateAddress
	}
	if ip, err := netip.ParseAddr(host); err == nil && blockedIP(ip) {
		return ErrPrivateAddress
	}
	return nil
}

// newClient is the HTTP client deliveries use. Without allowPrivate it
// refuses to connect to blocked addresses, checked on the address actually
// dialled (after DNS), so a name that resolves inside can't slip through.
// It follows no redirects and ignores proxy settings.
func newClient(timeout time.Duration, allowPrivate bool) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if !allowPrivate {
		d.Control = func(network, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || blockedIP(ap.Addr()) {
				return fmt.Errorf("%s: %w", address, ErrPrivateAddress)
			}
			return nil
		}
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           d.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          20,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: timeout,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
