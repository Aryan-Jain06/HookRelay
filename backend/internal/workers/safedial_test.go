package workers

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIPBlocked(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ip      string
		blocked bool
		why     string
	}{
		{"169.254.169.254", true, "cloud instance metadata"},
		{"169.254.0.1", true, "link-local"},
		{"127.0.0.1", true, "loopback"},
		{"127.1.2.3", true, "loopback range"},
		{"0.0.0.0", true, "unspecified"},
		{"10.0.0.5", true, "RFC1918 /8"},
		{"172.16.0.1", true, "RFC1918 /12"},
		{"172.31.255.255", true, "RFC1918 /12 upper bound"},
		{"192.168.1.1", true, "RFC1918 /16"},
		{"100.64.0.1", true, "carrier-grade NAT"},
		{"224.0.0.1", true, "multicast"},
		{"::1", true, "IPv6 loopback"},
		{"fd00::1", true, "IPv6 unique-local"},
		{"fe80::1", true, "IPv6 link-local"},

		{"1.1.1.1", false, "public resolver"},
		{"93.184.216.34", false, "public host"},
		{"8.8.8.8", false, "public resolver"},
		{"172.32.0.1", false, "just above RFC1918 /12"},
		{"11.0.0.1", false, "just above RFC1918 /8"},
		{"2606:4700:4700::1111", false, "public IPv6"},
	}
	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("test case %q is not a valid IP", tc.ip)
		}
		if got := ipBlocked(ip); got != tc.blocked {
			t.Errorf("ipBlocked(%s) = %v, want %v (%s)", tc.ip, got, tc.blocked, tc.why)
		}
	}
}

func TestIPBlockedRejectsUnparseable(t *testing.T) {
	t.Parallel()
	// A nil IP means the dial address did not parse. Failing closed is the only
	// safe reading.
	if !ipBlocked(nil) {
		t.Error("ipBlocked(nil) = false, want true: an unparseable address must fail closed")
	}
}

func TestSafeDialerRefusesInternalAddress(t *testing.T) {
	t.Parallel()
	client := &http.Client{
		Transport: deliveryTransport(false),
		Timeout:   3 * time.Second,
	}
	_, err := client.Get("http://169.254.169.254/latest/meta-data/")
	if err == nil {
		t.Fatal("request to the metadata address succeeded; the SSRF guard is not active")
	}
	if !strings.Contains(err.Error(), "refusing to deliver to internal address") {
		t.Fatalf("got %v, want a refusal from the safe dialler", err)
	}
}

func TestSafeDialerAllowsPublicAddress(t *testing.T) {
	t.Parallel()
	// httptest listens on loopback, which the guard blocks by design, so this
	// asserts the guard is what rejects it and that the escape hatch lifts it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	guarded := &http.Client{Transport: deliveryTransport(false), Timeout: 3 * time.Second}
	if _, err := guarded.Get(srv.URL); err == nil {
		t.Error("guarded client reached a loopback server; it should have been refused")
	}

	permitted := &http.Client{Transport: deliveryTransport(true), Timeout: 3 * time.Second}
	resp, err := permitted.Get(srv.URL)
	if err != nil {
		t.Fatalf("allowPrivate client could not reach loopback: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
