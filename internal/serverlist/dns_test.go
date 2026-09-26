package serverlist

import (
	"net"
	"net/netip"
	"testing"

	"github.com/miekg/dns"
)

// fakeWriter captures the reply and lets the test choose the client address.
type fakeWriter struct {
	remote net.Addr
	msg    *dns.Msg
}

func (f *fakeWriter) LocalAddr() net.Addr         { return &net.UDPAddr{IP: net.IPv4(10, 0, 1, 116), Port: 53} }
func (f *fakeWriter) RemoteAddr() net.Addr        { return f.remote }
func (f *fakeWriter) WriteMsg(m *dns.Msg) error   { f.msg = m; return nil }
func (f *fakeWriter) Write(b []byte) (int, error) { return len(b), nil }
func (f *fakeWriter) Close() error                { return nil }
func (f *fakeWriter) TsigStatus() error           { return nil }
func (f *fakeWriter) TsigTimersOnly(bool)         {}
func (f *fakeWriter) Hijack()                     {}
func (f *fakeWriter) Network() string             { return "udp" }

func ask(t *testing.T, d *dnsServer, from string, name string, qtype uint16) *dns.Msg {
	t.Helper()
	w := &fakeWriter{remote: &net.UDPAddr{IP: net.ParseIP(from), Port: 40000}}
	q := new(dns.Msg)
	q.SetQuestion(dns.Fqdn(name), qtype)
	d.ServeDNS(w, q)
	if w.msg == nil {
		t.Fatalf("no reply for %s from %s", name, from)
	}
	return w.msg
}

func testConfig(answerWorld bool) *Config {
	return &Config{
		ListIP:         netip.MustParseAddr("10.0.1.116"),
		PublicIP:       netip.MustParseAddr("203.0.113.7"),
		DNSUpstream:    "127.0.0.1:1", // nothing listens here; forwarded lookups fail fast
		DNSAnswerWorld: answerWorld,
		RedirectNames:  defaultRedirectNames,
	}
}

func TestHomeConsoleGetsListIP(t *testing.T) {
	d := newDNSServer(testConfig(false))
	m := ask(t, d, "10.0.1.20", "geo.hivebedrock.network", dns.TypeA)
	if len(m.Answer) != 1 || m.Answer[0].(*dns.A).A.String() != "10.0.1.116" {
		t.Fatalf("want 10.0.1.116, got %v", m.Answer)
	}
	// Case must not matter.
	m = ask(t, d, "192.168.1.5", "GEO.HiveBedrock.Network", dns.TypeA)
	if len(m.Answer) != 1 {
		t.Fatalf("mixed-case name was not redirected: %v", m)
	}
}

func TestIPv6LookupGetsEmptyAnswer(t *testing.T) {
	d := newDNSServer(testConfig(false))
	m := ask(t, d, "10.0.1.20", "mco.lbsg.net", dns.TypeAAAA)
	if m.Rcode != dns.RcodeSuccess || len(m.Answer) != 0 {
		t.Fatalf("want empty success for AAAA, got rcode %d answers %v", m.Rcode, m.Answer)
	}
}

func TestInternetClientRefusedInHomeMode(t *testing.T) {
	d := newDNSServer(testConfig(false))
	m := ask(t, d, "8.8.4.4", "geo.hivebedrock.network", dns.TypeA)
	if m.Rcode != dns.RcodeRefused {
		t.Fatalf("home mode should refuse internet clients, got rcode %d", m.Rcode)
	}
}

func TestInternetClientGetsPublicIPInFriendsMode(t *testing.T) {
	d := newDNSServer(testConfig(true))
	m := ask(t, d, "8.8.4.4", "play.galaxite.net", dns.TypeA)
	if len(m.Answer) != 1 || m.Answer[0].(*dns.A).A.String() != "203.0.113.7" {
		t.Fatalf("want public IP, got %v", m.Answer)
	}
}

func TestNeverAnOpenResolver(t *testing.T) {
	d := newDNSServer(testConfig(true))
	m := ask(t, d, "8.8.4.4", "example.com", dns.TypeA)
	if m.Rcode != dns.RcodeRefused {
		t.Fatalf("internet lookups of other names must be refused, got rcode %d", m.Rcode)
	}
}

func TestHomeLookupsAreForwarded(t *testing.T) {
	d := newDNSServer(testConfig(false))
	m := ask(t, d, "10.0.1.20", "example.com", dns.TypeA)
	// The upstream in the test config is unreachable, so a forwarded lookup
	// comes back as SERVFAIL rather than REFUSED.
	if m.Rcode != dns.RcodeServerFailure {
		t.Fatalf("home lookups should be forwarded, got rcode %d", m.Rcode)
	}
}

func TestRateLimitForInternetClients(t *testing.T) {
	d := newDNSServer(testConfig(true))
	refused := 0
	for i := 0; i < 300; i++ {
		if ask(t, d, "8.8.4.4", "play.inpvp.net", dns.TypeA).Rcode == dns.RcodeRefused {
			refused++
		}
	}
	if refused == 0 {
		t.Fatal("expected a flood from one internet address to be limited")
	}
	for i := 0; i < 300; i++ {
		if ask(t, d, "10.0.1.20", "play.inpvp.net", dns.TypeA).Rcode != dns.RcodeSuccess {
			t.Fatal("home devices must never be rate limited")
		}
	}
}

func TestEmptyChunkPayloadShape(t *testing.T) {
	// 2 bytes for the first biome section, 23 "copy previous" markers, 1 border byte.
	if len(emptyChunkPayload) != 26 || emptyChunkPayload[25] != 0 {
		t.Fatalf("unexpected empty chunk payload: %v", emptyChunkPayload)
	}
}
