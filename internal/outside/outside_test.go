package outside

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRouter is a UPnP gateway that keeps its forwards in a map.
type fakeRouter struct {
	mu       sync.Mutex
	forwards map[string]string // "UDP/19144" -> internal client
	leases   map[string]string
	external string
	permOnly bool
	refuse   map[string]int // port -> UPnP error code
	calls    []string
	srv      *httptest.Server
}

func newFakeRouter(t *testing.T) *fakeRouter {
	f := &fakeRouter{forwards: map[string]string{}, leases: map[string]string{}, refuse: map[string]int{}, external: "203.0.113.5"}
	mux := http.NewServeMux()
	mux.HandleFunc("/desc.xml", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><root xmlns="urn:schemas-upnp-org:device-1-0"><device>
<deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:1</deviceType><friendlyName>Test Router</friendlyName>
<deviceList><device><deviceType>urn:schemas-upnp-org:device:WANDevice:1</deviceType><deviceList><device>
<deviceType>urn:schemas-upnp-org:device:WANConnectionDevice:1</deviceType>
<serviceList><service><serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType><controlURL>/ctl</controlURL></service></serviceList>
</device></deviceList></device></deviceList></device></root>`)
	})
	mux.HandleFunc("/ctl", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		v := soapValues(b)
		action := r.Header.Get("SOAPAction")
		action = strings.Trim(action[strings.Index(action, "#")+1:], `"`)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, action)
		k := v["NewProtocol"] + "/" + v["NewExternalPort"]
		fail := func(code int) {
			w.WriteHeader(500)
			fmt.Fprintf(w, `<s:Envelope><s:Body><s:Fault><detail><UPnPError><errorCode>%d</errorCode><errorDescription>nope</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`, code)
		}
		switch action {
		case "GetExternalIPAddress":
			fmt.Fprintf(w, `<s:Envelope><s:Body><u:GetExternalIPAddressResponse><NewExternalIPAddress>%s</NewExternalIPAddress></u:GetExternalIPAddressResponse></s:Body></s:Envelope>`, f.external)
		case "AddPortMapping":
			if c := f.refuse[v["NewExternalPort"]]; c != 0 {
				fail(c)
				return
			}
			if f.permOnly && v["NewLeaseDuration"] != "0" {
				fail(725)
				return
			}
			f.forwards[k] = v["NewInternalClient"]
			f.leases[k] = v["NewLeaseDuration"]
			fmt.Fprint(w, `<s:Envelope><s:Body><u:AddPortMappingResponse/></s:Body></s:Envelope>`)
		case "GetSpecificPortMappingEntry":
			c, ok := f.forwards[k]
			if !ok {
				fail(714)
				return
			}
			fmt.Fprintf(w, `<s:Envelope><s:Body><u:R><NewInternalPort>1</NewInternalPort><NewInternalClient>%s</NewInternalClient></u:R></s:Body></s:Envelope>`, c)
		case "DeletePortMapping":
			if _, ok := f.forwards[k]; !ok {
				fail(714)
				return
			}
			delete(f.forwards, k)
			fmt.Fprint(w, `<s:Envelope><s:Body><u:DeletePortMappingResponse/></s:Body></s:Envelope>`)
		default:
			fail(401)
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRouter) has(k string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.forwards[k]
	return ok
}

func TestRouterForwards(t *testing.T) {
	f := newFakeRouter(t)
	ctx := context.Background()
	g, err := discover(ctx, netip.Addr{}, f.srv.URL+"/desc.xml")
	if err != nil {
		t.Fatal(err)
	}
	if g.name != "Test Router" || !strings.HasSuffix(g.control, "/ctl") {
		t.Errorf("gateway %+v", g)
	}
	if ip, err := g.externalIP(ctx); err != nil || ip.String() != "203.0.113.5" {
		t.Errorf("external %v %v", ip, err)
	}
	local := netip.MustParseAddr("10.0.1.117")
	if perm, err := g.addMapping(ctx, Port{19144, "UDP", "Kids <Survival>"}, local); err != nil || perm {
		t.Fatalf("add: %v %v", perm, err)
	}
	if f.forwards["UDP/19144"] != "10.0.1.117" || f.leases["UDP/19144"] != "3600" {
		t.Errorf("forwards %v %v", f.forwards, f.leases)
	}
	f.permOnly = true
	if perm, err := g.addMapping(ctx, Port{19132, "UDP", "list"}, local); err != nil || !perm || f.leases["UDP/19132"] != "0" {
		t.Errorf("permanent-only router: %v %v %v", perm, err, f.leases)
	}
	f.refuse["53"] = 718
	if _, err := g.addMapping(ctx, Port{53, "UDP", "dns"}, local); err == nil || !strings.Contains(err.Error(), "another device") {
		t.Errorf("conflict: %v", err)
	}
	if err := g.deleteMapping(ctx, Port{19144, "UDP", ""}); err != nil || f.has("UDP/19144") {
		t.Errorf("delete: %v", err)
	}
	if err := g.deleteMapping(ctx, Port{19144, "UDP", ""}); err != nil {
		t.Errorf("deleting a forward that's gone: %v", err)
	}
}

func TestCheckAddress(t *testing.T) {
	for in, want := range map[string]string{
		"":                        "",
		" MC.Example.com ":        "mc.example.com",
		"https://mc.example.com/": "mc.example.com",
		"203.0.113.9":             "203.0.113.9",
	} {
		if got, err := CheckAddress(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"192.168.1.5", "100.64.3.2", "localhost", "mc example.com", "::1", "a..b"} {
		if _, err := CheckAddress(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestManagerOpensWhatsOpen(t *testing.T) {
	f := newFakeRouter(t)
	ipSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "203.0.113.5\n") }))
	defer ipSrv.Close()
	dir := t.TempDir()
	var mu sync.Mutex
	list := []Server{
		{ID: "kids", Name: "Kids Survival", Port: 19144, Open: true},
		{ID: "dad", Name: "Schneck World", Port: 19142},
		{ID: "risky", Name: "Test", Port: 19134, Open: true, Problem: "the allowlist is off"},
	}
	opts := Options{
		DataDir: dir, LocalIP: netip.MustParseAddr("10.0.1.117"), ListPort: 19132, DNSPort: 53,
		LookupURLs: []string{ipSrv.URL}, GatewayURL: f.srv.URL + "/desc.xml",
		Servers: func() []Server { mu.Lock(); defer mu.Unlock(); return append([]Server(nil), list...) },
	}
	m := New(opts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	refresh := func() Status {
		c, cl := context.WithTimeout(context.Background(), 10*time.Second)
		defer cl()
		return m.Refresh(c)
	}

	// Off: nothing is open.
	st := refresh()
	if len(f.forwards) != 0 || m.Info().Enabled || m.Info().Open(19144) {
		t.Errorf("off: %v %+v", f.forwards, m.Info())
	}
	if err := m.SetSettings(Settings{Enabled: true, UPnP: true, Address: "203.0.113.5"}); err != nil {
		t.Fatal(err)
	}
	st = refresh()
	if !f.has("UDP/19144") || f.has("UDP/19134") || f.has("UDP/19142") || f.has("UDP/19132") {
		t.Errorf("forwards: %v", f.forwards)
	}
	info := m.Info()
	if !info.Enabled || info.Host != "203.0.113.5" || !info.Open(19144) || info.Open(19134) || info.Consoles {
		t.Errorf("info: %+v", info)
	}
	if st.PublicIP != "203.0.113.5" || st.Shared || st.Router != "Test Router" || st.AddressWarning != "" {
		t.Errorf("status: %+v", st)
	}
	// Consoles need the list and DNS too.
	m.SetSettings(Settings{Enabled: true, UPnP: true, Consoles: true})
	st = refresh()
	for _, k := range []string{"UDP/19144", "UDP/19132", "UDP/53", "TCP/53"} {
		if !f.has(k) {
			t.Errorf("%s not forwarded: %v", k, f.forwards)
		}
	}
	if !m.Info().Consoles || m.Info().IP.String() != "203.0.113.5" {
		t.Errorf("consoles: %+v", m.Info())
	}
	// Closing a server closes its forward; the router's own forwards stay.
	f.mu.Lock()
	f.forwards["UDP/25565"] = "10.0.1.50"
	f.mu.Unlock()
	mu.Lock()
	list[0].Open = false
	mu.Unlock()
	refresh()
	if f.has("UDP/19144") || !f.has("UDP/25565") || m.Info().Open(19144) {
		t.Errorf("after closing: %v", f.forwards)
	}
	// A second router in front: the address the internet sees isn't the router's.
	f.mu.Lock()
	f.external = "192.168.0.20"
	f.mu.Unlock()
	if st = refresh(); !st.Shared {
		t.Errorf("double router not noticed: %+v", st)
	}
	// Forwards made are remembered, and closed when the panel stops.
	m2 := New(opts)
	if len(m2.mapped) != 3 {
		t.Errorf("remembered %v", m2.mapped)
	}
	// A router that forgets its forwards gets them again (after renewAfter).
	f.mu.Lock()
	delete(f.forwards, "UDP/19132")
	f.mu.Unlock()
	m.passMu.Lock()
	for k, x := range m.mapped {
		x.At = time.Now().Add(-renewAfter - time.Minute)
		m.mapped[k] = x
	}
	m.passMu.Unlock()
	refresh()
	if !f.has("UDP/19132") {
		t.Errorf("forgotten forward not made again: %v", f.forwards)
	}
	// Another device took TCP 53 meanwhile: stopping mustn't close its forward.
	f.mu.Lock()
	f.forwards["TCP/53"] = "10.0.1.50"
	f.mu.Unlock()
	cancel()
	<-done
	if f.has("UDP/19132") || !f.has("TCP/53") || !f.has("UDP/25565") {
		t.Errorf("after stopping: %v", f.forwards)
	}
}
