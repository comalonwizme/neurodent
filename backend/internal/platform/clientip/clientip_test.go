package clientip_test

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/comalonwizme/neurodent/backend/internal/platform/clientip"
)

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("fd00::/8")}
	tests := []struct {
		name    string
		trusted []netip.Prefix
		remote  string
		xff     []string
		want    string
	}{
		{"no proxies configured: XFF ignored", nil, "10.1.1.1:5000", []string{"203.0.113.9"}, "10.1.1.1"},
		{"untrusted peer: XFF ignored", trusted, "198.51.100.7:5000", []string{"203.0.113.9"}, "198.51.100.7"},
		{"trusted peer: client from XFF", trusted, "10.1.1.1:5000", []string{"203.0.113.9"}, "203.0.113.9"},
		{"spoofed leftmost entry is not the client", trusted, "10.1.1.1:5000", []string{"1.2.3.4, 203.0.113.9"}, "203.0.113.9"},
		{"chain of trusted proxies", trusted, "10.1.1.1:5000", []string{"203.0.113.9, 10.2.2.2, 10.3.3.3"}, "203.0.113.9"},
		{"several XFF headers in order", trusted, "10.1.1.1:5000", []string{"1.2.3.4", "203.0.113.9, 10.2.2.2"}, "203.0.113.9"},
		{"garbage stops trust at the last good hop", trusted, "10.1.1.1:5000", []string{"203.0.113.9, junk, 10.2.2.2"}, "10.2.2.2"},
		{"all trusted: leftmost", trusted, "10.1.1.1:5000", []string{"10.9.9.9, 10.2.2.2"}, "10.9.9.9"},
		{"trusted peer without XFF", trusted, "10.1.1.1:5000", nil, "10.1.1.1"},
		{"IPv6 peer and client", trusted, "[fd00::1]:5000", []string{"2001:db8::7"}, "2001:db8::7"},
		{"IPv4-mapped IPv6 unmapped", nil, "[::ffff:198.51.100.7]:5000", nil, "198.51.100.7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remote
			for _, v := range tt.xff {
				req.Header.Add("X-Forwarded-For", v)
			}
			got, ok := clientip.NewResolver(tt.trusted).ClientIP(req)
			if !ok || got.String() != tt.want {
				t.Errorf("ClientIP() = %v, %v; want %s", got, ok, tt.want)
			}
		})
	}
}

func TestClientIP_UnparsableRemote(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "@unix"
	if _, ok := clientip.NewResolver(nil).ClientIP(req); ok {
		t.Error("unix socket peer resolved to an IP")
	}
}

func TestLimitKey(t *testing.T) {
	tests := map[string]string{
		"203.0.113.9":               "203.0.113.9",
		"2001:db8:1:2:aaaa::1":      "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff:ffff::9": "2001:db8:1:2::/64",
	}
	for in, want := range tests {
		if got := clientip.LimitKey(netip.MustParseAddr(in)); got != want {
			t.Errorf("LimitKey(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestContext(t *testing.T) {
	a := netip.MustParseAddr("203.0.113.9")
	if got, ok := clientip.FromContext(clientip.WithIP(t.Context(), a)); !ok || got != a {
		t.Errorf("FromContext = %v, %v", got, ok)
	}
	if _, ok := clientip.FromContext(t.Context()); ok {
		t.Error("empty context has an IP")
	}
}
