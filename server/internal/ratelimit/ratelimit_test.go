package ratelimit

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	cases := []struct {
		name, remote string
		xff          []string
		want         string
	}{
		{"untrusted peer ignores header", "203.0.113.9:1234", []string{"1.2.3.4"}, "203.0.113.9"},
		{"trusted peer, one hop", "10.0.0.2:1", []string{"198.51.100.7"}, "198.51.100.7"},
		{"spoofed left entries ignored", "10.0.0.2:1", []string{"6.6.6.6, 198.51.100.7"}, "198.51.100.7"},
		{"trusted chain skipped", "10.0.0.2:1", []string{"198.51.100.7, 10.1.1.1"}, "198.51.100.7"},
		{"multiple headers", "10.0.0.2:1", []string{"6.6.6.6", "198.51.100.7"}, "198.51.100.7"},
		{"all trusted", "10.0.0.2:1", []string{"10.3.3.3"}, "10.3.3.3"},
		{"no header", "10.0.0.2:1", nil, "10.0.0.2"},
		{"garbage hop", "10.0.0.2:1", []string{"nonsense"}, "10.0.0.2"},
		{"ipv6 loopback proxy", "[::1]:80", []string{"2001:db8::1"}, "2001:db8::1"},
		{"mapped ipv4", "[::ffff:203.0.113.9]:1", nil, "203.0.113.9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = c.remote
			for _, h := range c.xff {
				r.Header.Add("X-Forwarded-For", h)
			}
			if got := ClientIP(r, trusted); got.String() != c.want {
				t.Errorf("got %s, want %s", got, c.want)
			}
		})
	}
}

func TestKey(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.9":         "203.0.113.9",
		"2001:db8:1:2::1":     "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::": "2001:db8:1:2::/64",
		"2001:db8:1:3::1":     "2001:db8:1:3::/64",
	} {
		if got := Key(netip.MustParseAddr(in)); got != want {
			t.Errorf("Key(%s) = %s, want %s", in, got, want)
		}
	}
}
