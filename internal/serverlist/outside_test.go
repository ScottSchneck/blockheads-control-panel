package serverlist

import (
	"net"
	"net/netip"
	"testing"

	"github.com/miekg/dns"
)

func TestFriendsOutsideFollowThePanel(t *testing.T) {
	cfg := testConfig(false)
	cfg.PublicIP = netip.Addr{}
	o := Outside{}
	cfg.OutsideInfo = func() Outside { return o }
	d := newDNSServer(cfg)
	home := &net.UDPAddr{IP: net.ParseIP("10.0.1.20"), Port: 5000}
	away := &net.UDPAddr{IP: net.ParseIP("198.51.100.7"), Port: 5000}

	// Off: consoles outside get nothing, and home is unchanged.
	if m := ask(t, d, "198.51.100.7", "geo.hivebedrock.network", dns.TypeA); m.Rcode != dns.RcodeRefused {
		t.Errorf("answered while off: %v", m)
	}
	if h := transferHost(cfg, away); h != "10.0.1.116" {
		t.Errorf("transfer while off: %s", h)
	}

	// On, for PCs and phones only: consoles' DNS still isn't answered.
	o = Outside{Enabled: true, Host: "mc.example.com", IP: netip.MustParseAddr("203.0.113.9")}
	if m := ask(t, d, "198.51.100.7", "geo.hivebedrock.network", dns.TypeA); m.Rcode != dns.RcodeRefused {
		t.Errorf("answered without consoles on: %v", m)
	}
	if h := transferHost(cfg, away); h != "203.0.113.9" {
		t.Errorf("transfer outside: %s", h)
	}
	if h := transferHost(cfg, home); h != "10.0.1.116" {
		t.Errorf("transfer at home: %s", h)
	}

	// With consoles on, the featured names lead to the home, and nothing else is answered.
	o.Consoles = true
	m := ask(t, d, "198.51.100.7", "geo.hivebedrock.network", dns.TypeA)
	if len(m.Answer) != 1 || m.Answer[0].(*dns.A).A.String() != "203.0.113.9" {
		t.Errorf("console outside: %v", m)
	}
	if m := ask(t, d, "198.51.100.7", "example.com", dns.TypeA); m.Rcode != dns.RcodeRefused {
		t.Errorf("open resolver: %v", m)
	}
	if m := ask(t, d, "10.0.1.20", "geo.hivebedrock.network", dns.TypeA); m.Answer[0].(*dns.A).A.String() != "10.0.1.116" {
		t.Errorf("home console: %v", m)
	}
}
