package workers

import (
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

// blockedNets are the ranges a webhook delivery must never reach: loopback,
// link-local (cloud metadata lives at 169.254.169.254), carrier-grade NAT and
// RFC1918 private space.
//
// Endpoint URLs are supplied by tenants, so without this guard a tenant can
// register http://169.254.169.254/ and have HookRelay read the instance's cloud
// credentials on their behalf — an authenticated SSRF proxy into the network
// HookRelay runs in.
var blockedNets = func() []*net.IPNet {
	cidrs := []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
		"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16",
		"198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4",
		"::1/128", "fc00::/7", "fe80::/10", "::/128",
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			// The list is a compile-time constant; a bad entry is a programming
			// error, and silently dropping it would weaken the guard.
			panic(fmt.Sprintf("workers: invalid blocked CIDR %q: %v", c, err))
		}
		out = append(out, n)
	}
	return out
}()

// ipBlocked reports whether ip is an address deliveries must not reach.
func ipBlocked(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	for _, n := range blockedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// safeDialer refuses to connect to internal addresses.
//
// The check runs in Control rather than against the URL's hostname, because
// Control fires after DNS resolution with the concrete address about to be
// dialled. A public hostname whose A record points at 169.254.169.254 is
// therefore still caught, and because the check and the connect share the same
// resolved address there is no resolve-then-connect window to race.
func safeDialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("parse dial address %q: %w", address, err)
			}
			if ip := net.ParseIP(host); ipBlocked(ip) {
				return fmt.Errorf("refusing to deliver to internal address %s", ip)
			}
			return nil
		},
	}
}

// deliveryTransport builds the transport used for delivery attempts. The SSRF
// guard is applied unless allowPrivate is set, so the safe behaviour is what you
// get by default and the escape hatch has to be asked for by name.
func deliveryTransport(allowPrivate bool) *http.Transport {
	tr := &http.Transport{
		MaxIdleConns:        512,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     90 * time.Second,
	}
	if !allowPrivate {
		tr.DialContext = safeDialer(5 * time.Second).DialContext
	}
	return tr
}
