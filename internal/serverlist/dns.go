package serverlist

import (
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// dnsServer answers the featured-server names with the list server's address
// and, for devices on the home network only, forwards everything else to an
// upstream resolver. Lookups from the internet for other names are refused,
// so this can never be used as an open resolver.
type dnsServer struct {
	cfg      *Config
	redirect map[string]bool
	client   *dns.Client
	tcp      *dns.Client

	limitMu sync.Mutex
	limits  map[netip.Addr]*bucket

	seenMu sync.Mutex
	seen   map[string]time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func isHomeAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || cgnat.Contains(a)
}

func newDNSServer(cfg *Config) *dnsServer {
	d := &dnsServer{
		cfg:      cfg,
		redirect: map[string]bool{},
		client:   &dns.Client{Net: "udp", Timeout: 3 * time.Second},
		tcp:      &dns.Client{Net: "tcp", Timeout: 3 * time.Second},
		limits:   map[netip.Addr]*bucket{},
		seen:     map[string]time.Time{},
	}
	for _, n := range cfg.RedirectNames {
		d.redirect[dns.Fqdn(strings.ToLower(n))] = true
	}
	return d
}

func (d *dnsServer) start() error {
	addr := net.JoinHostPort("", strconv.Itoa(d.cfg.DNSPort))
	errs := make(chan error, 2)
	for _, network := range []string{"udp", "tcp"} {
		srv := &dns.Server{Addr: addr, Net: network, Handler: d}
		started := make(chan struct{})
		srv.NotifyStartedFunc = func() { close(started) }
		go func() { errs <- srv.ListenAndServe() }()
		select {
		case <-started:
		case err := <-errs:
			return err
		case <-time.After(3 * time.Second):
		}
	}
	slog.Info("built-in DNS listening", "port", d.cfg.DNSPort, "upstream", d.cfg.DNSUpstream,
		"answersInternet", d.cfg.DNSAnswerWorld, "names", d.cfg.RedirectNames)
	return nil
}

// allow applies a simple per-address rate limit: 20 queries per second with a
// burst of 100. Home devices are never limited.
func (d *dnsServer) allow(a netip.Addr) bool {
	if isHomeAddr(a) {
		return true
	}
	d.limitMu.Lock()
	defer d.limitMu.Unlock()
	now := time.Now()
	b, ok := d.limits[a]
	if !ok {
		if len(d.limits) > 10000 {
			d.limits = map[netip.Addr]*bucket{}
		}
		b = &bucket{tokens: 100, last: now}
		d.limits[a] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * 20
	if b.tokens > 100 {
		b.tokens = 100
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func clientAddr(w dns.ResponseWriter) netip.Addr {
	if ap, err := netip.ParseAddrPort(w.RemoteAddr().String()); err == nil {
		return ap.Addr().Unmap()
	}
	return netip.Addr{}
}

func (d *dnsServer) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	from := clientAddr(w)
	if len(r.Question) != 1 || !d.allow(from) {
		d.reply(w, r, dns.RcodeRefused)
		return
	}
	q := r.Question[0]
	name := strings.ToLower(q.Name)
	home := isHomeAddr(from)

	if d.redirect[name] {
		d.answerRedirect(w, r, q, from, home)
		return
	}
	if !home {
		slog.Debug("DNS refused internet lookup", "from", from, "name", name)
		d.reply(w, r, dns.RcodeRefused)
		return
	}
	d.forward(w, r, from)
}

func (d *dnsServer) answerRedirect(w dns.ResponseWriter, r *dns.Msg, q dns.Question, from netip.Addr, home bool) {
	target := d.cfg.ListIP
	if !home {
		if !d.cfg.DNSAnswerWorld || !d.cfg.PublicIP.IsValid() {
			d.reply(w, r, dns.RcodeRefused)
			return
		}
		target = d.cfg.PublicIP
	}

	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	switch q.Qtype {
	case dns.TypeA, dns.TypeANY:
		m.Answer = append(m.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.IP(target.AsSlice()),
		})
	default:
		// AAAA and everything else: an empty answer. This stops consoles on
		// IPv6 networks from skipping the redirect.
	}
	_ = w.WriteMsg(m)

	// Log each console/name pair once a minute so the test log stays readable.
	key := from.String() + " " + q.Name + " " + dns.TypeToString[q.Qtype]
	d.seenMu.Lock()
	last, ok := d.seen[key]
	if !ok || time.Since(last) > time.Minute {
		d.seen[key] = time.Now()
		d.seenMu.Unlock()
		slog.Info("DNS sent a console to the server list", "from", from, "name", strings.TrimSuffix(q.Name, "."),
			"type", dns.TypeToString[q.Qtype], "answer", target)
		return
	}
	d.seenMu.Unlock()
}

func (d *dnsServer) forward(w dns.ResponseWriter, r *dns.Msg, from netip.Addr) {
	resp, _, err := d.client.Exchange(r, d.cfg.DNSUpstream)
	if err == nil && resp != nil && resp.Truncated {
		resp, _, err = d.tcp.Exchange(r, d.cfg.DNSUpstream)
	}
	if err != nil || resp == nil {
		slog.Debug("DNS upstream failed", "from", from, "name", r.Question[0].Name, "error", err)
		d.reply(w, r, dns.RcodeServerFailure)
		return
	}
	resp.Id = r.Id
	_ = w.WriteMsg(resp)
}

func (d *dnsServer) reply(w dns.ResponseWriter, r *dns.Msg, rcode int) {
	m := new(dns.Msg)
	m.SetRcode(r, rcode)
	_ = w.WriteMsg(m)
}
