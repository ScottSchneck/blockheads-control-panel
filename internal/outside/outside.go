// Package outside lets friends outside the house join: it keeps track of the
// home's internet address, which servers are open to the internet, and the
// port forwards they need on the router, opening them with UPnP when the
// owner allows it.
//
// A server is only ever open to the internet while its allowlist and Xbox
// sign-in are on (the servers package says whether that's so). Everything is
// worked out again every minute, so a change made anywhere (a setting, a
// server that's added or removed, the router forgetting a forward) is
// corrected on its own.
package outside

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Settings are the owner's choices, kept in <data>/outside.json.
type Settings struct {
	// Enabled lets friends outside the house join servers that are open.
	Enabled bool `json:"enabled"`
	// Address is what friends type: a hostname (such as a dynamic DNS name)
	// or an IP address. Empty means the home's internet address.
	Address string `json:"address"`
	// UPnP opens the port forwards on the router automatically.
	UPnP bool `json:"upnp"`
	// Consoles lets friends' consoles use the server list from outside
	// (their DNS pointed at this home's address).
	Consoles bool `json:"consoles"`
}

// Server is one of the panel's servers, as far as this package cares.
type Server struct {
	ID      string
	Name    string
	Port    int
	Open    bool   // the server is marked open to friends outside
	Problem string // why it can't be open right now ("the allowlist is off"), or ""
}

// Port is one forward on the router.
type Port struct {
	Port  int    `json:"port"`
	Proto string `json:"proto"` // UDP or TCP
	For   string `json:"for"`   // what it's for, such as a server's name
}

// PortState is a forward and whether the router has it.
type PortState struct {
	Port
	Opened bool   `json:"opened"` // the router said yes (UPnP)
	Error  string `json:"error,omitempty"`
}

// ServerState is a server's standing with the outside.
type ServerState struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Port    int    `json:"port"`
	Open    bool   `json:"open"`    // marked open
	Reached bool   `json:"reached"` // open and nothing stops it (panel on, no problem)
	Problem string `json:"problem,omitempty"`
}

// Status is everything the panel shows about outside access.
type Status struct {
	Settings Settings `json:"settings"`
	LocalIP  string   `json:"localIP"` // where the router should forward to
	// PublicIP is the home's internet address, and where it came from
	// ("internet", "router" or "setting").
	PublicIP      string `json:"publicIP,omitempty"`
	PublicIPFrom  string `json:"publicIPFrom,omitempty"`
	PublicIPError string `json:"publicIPError,omitempty"`
	// Shared is true when the internet provider (or a second router) shares
	// the address, so forwards on this router can't be reached.
	Shared bool `json:"shared"`
	// Address is what friends type; AddressIP is where it leads.
	Address        string `json:"address,omitempty"`
	AddressIP      string `json:"addressIP,omitempty"`
	AddressWarning string `json:"addressWarning,omitempty"`
	Router         string `json:"router,omitempty"`
	RouterError    string `json:"routerError,omitempty"`
	// Ports are the forwards friends need, and what the router said.
	Ports   []PortState   `json:"ports"`
	Servers []ServerState `json:"servers"`
	Checked time.Time     `json:"checked"`
}

// Info is what the console server list needs.
type Info struct {
	Enabled  bool       // friends outside may join
	Host     string     // what their consoles are sent to
	IP       netip.Addr // the home's address, for DNS answers
	Consoles bool       // answer consoles' DNS lookups from outside
	open     map[int]bool
}

// Open reports whether the server on port is open to friends outside.
func (i Info) Open(port int) bool { return i.Enabled && i.open[port] }

// Options configure a Manager.
type Options struct {
	DataDir  string
	LocalIP  netip.Addr // this container's address on the home network
	ListPort int        // the console server list (19132)
	DNSPort  int        // the built-in DNS (53); 0 when it's off
	// StaticIP is the home's address when it's set by hand (PUBLIC_IP).
	StaticIP netip.Addr
	// LookupURLs answer with the caller's internet address as plain text.
	// Empty means don't look it up that way.
	LookupURLs []string
	// GatewayURL skips searching for the router (UPNP_GATEWAY).
	GatewayURL string
	// Servers lists the panel's servers.
	Servers func() []Server
	Log     *slog.Logger
}

// DefaultLookupURLs are asked for the home's internet address, in turn.
var DefaultLookupURLs = []string{"https://api.ipify.org", "https://icanhazip.com", "https://ifconfig.me/ip"}

// Manager keeps outside access as the settings say.
type Manager struct {
	opts Options
	log  *slog.Logger

	mu       sync.Mutex
	settings Settings
	status   Status
	info     Info

	kick    chan chan struct{}
	running atomic.Bool
	passMu  sync.Mutex // one pass at a time

	// Used only by the loop.
	gw         *gateway
	mapped     map[string]mapping // key "UDP/19144": forwards this panel made
	lookedUp   netip.Addr
	lookupErr  string
	lookedUpAt time.Time
}

type mapping struct {
	Port
	At        time.Time `json:"at"`
	Permanent bool      `json:"permanent"`
	Client    string    `json:"client"` // the address it forwards to
}

func key(p Port) string { return p.Proto + "/" + strconv.Itoa(p.Port) }

// New reads the saved settings. Call Run to start keeping them.
func New(opts Options) *Manager {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{opts: opts, log: log.With("src", "outside"), kick: make(chan chan struct{}, 1), mapped: map[string]mapping{}}
	if b, err := os.ReadFile(m.settingsPath()); err == nil {
		_ = json.Unmarshal(b, &m.settings)
	}
	// Forwards made before a restart: kept if still wanted, removed if not.
	if b, err := os.ReadFile(m.mappingsPath()); err == nil {
		var list []mapping
		if json.Unmarshal(b, &list) == nil {
			for _, x := range list {
				m.mapped[key(x.Port)] = x
			}
		}
	}
	m.status = Status{Settings: m.settings, LocalIP: opts.LocalIP.String(), Ports: []PortState{}, Servers: []ServerState{}}
	return m
}

func (m *Manager) settingsPath() string { return filepath.Join(m.opts.DataDir, "outside.json") }
func (m *Manager) mappingsPath() string { return filepath.Join(m.opts.DataDir, "upnp-forwards.json") }

// Settings returns the owner's choices.
func (m *Manager) Settings() Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings
}

var hostnamePattern = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// CheckAddress tidies an address friends type, or says what's wrong.
func CheckAddress(a string) (string, error) {
	a = strings.TrimSpace(a)
	a = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(a, "http://"), "https://"), "/")
	if a == "" {
		return "", nil
	}
	if ip, err := netip.ParseAddr(a); err == nil {
		if !ip.Is4() {
			return "", errors.New("use an IPv4 address (four numbers with dots) or a hostname")
		}
		if isShared(ip) {
			return "", errors.New("that's an address inside a home network; friends outside can't reach it")
		}
		return ip.String(), nil
	}
	if len(a) > 253 || !hostnamePattern.MatchString(a) {
		return "", errors.New("that isn't a hostname (like mc.example.com) or an IP address")
	}
	return strings.ToLower(a), nil
}

// SetSettings saves new choices and applies them straight away.
func (m *Manager) SetSettings(s Settings) error {
	addr, err := CheckAddress(s.Address)
	if err != nil {
		return err
	}
	s.Address = addr
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := writeFile(m.settingsPath(), b); err != nil {
		return err
	}
	m.mu.Lock()
	m.settings = s
	m.status.Settings = s
	m.mu.Unlock()
	m.Kick()
	return nil
}

// Status returns the latest state.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.status
	st.Ports = append([]PortState(nil), st.Ports...)
	st.Servers = append([]ServerState(nil), st.Servers...)
	return st
}

// Info returns what the console server list needs.
func (m *Manager) Info() Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.info
}

// Kick asks for everything to be worked out again soon (after a server is
// opened or closed, say).
func (m *Manager) Kick() {
	select {
	case m.kick <- nil:
	default:
	}
}

// Refresh works everything out again and waits for it (up to ctx).
func (m *Manager) Refresh(ctx context.Context) Status {
	if !m.running.Load() {
		m.reconcile(ctx) // nothing else is keeping it: do it here
		return m.Status()
	}
	done := make(chan struct{})
	select {
	case m.kick <- done:
	case <-ctx.Done():
		return m.Status()
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
	return m.Status()
}

// Run keeps outside access as it should be until ctx ends, then closes the
// forwards it opened (the servers stop with the panel anyway).
func (m *Manager) Run(ctx context.Context) {
	m.running.Store(true)
	defer m.running.Store(false)
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	var waiting []chan struct{} // Refresh calls to answer after the next pass
	take := func(d chan struct{}) {
		if d != nil {
			waiting = append(waiting, d)
		}
	}
	for {
		m.reconcile(ctx)
		for _, d := range waiting {
			close(d)
		}
		waiting = nil
		select {
		case <-ctx.Done():
			m.closeAll()
			return
		case <-tick.C:
		case d := <-m.kick:
			take(d)
		}
		// Requests that came in meanwhile are answered by the same pass.
		for more := true; more; {
			select {
			case d := <-m.kick:
				take(d)
			default:
				more = false
			}
		}
	}
}

func (m *Manager) closeAll() {
	m.passMu.Lock()
	defer m.passMu.Unlock()
	if len(m.mapped) == 0 || m.gw == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for k, x := range m.mapped {
		if m.closeForward(ctx, x) == nil {
			delete(m.mapped, k)
		}
	}
	m.saveMappings()
	m.log.Info("closed the router's port forwards as the panel stops")
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// isShared reports whether an address can't be reached from the internet:
// a home network address, or one an internet provider shares (CGNAT).
func isShared(a netip.Addr) bool {
	a = a.Unmap()
	return !a.IsGlobalUnicast() || a.IsPrivate() || cgnat.Contains(a)
}

// reconcile works out what should be open and makes it so.
func (m *Manager) reconcile(ctx context.Context) {
	m.passMu.Lock()
	defer m.passMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	set := m.Settings()
	st := Status{Settings: set, LocalIP: m.opts.LocalIP.String(), Ports: []PortState{}, Servers: []ServerState{}, Checked: time.Now()}

	// Which servers are open, and the forwards they need.
	var want []Port
	open := map[int]bool{}
	var list []Server
	if m.opts.Servers != nil {
		list = m.opts.Servers()
	}
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	for _, s := range list {
		ss := ServerState{ID: s.ID, Name: s.Name, Port: s.Port, Open: s.Open, Problem: s.Problem}
		if s.Open && s.Problem == "" && set.Enabled {
			ss.Reached = true
			open[s.Port] = true
			want = append(want, Port{Port: s.Port, Proto: "UDP", For: s.Name})
		}
		st.Servers = append(st.Servers, ss)
	}
	if set.Enabled && set.Consoles {
		want = append(want, Port{Port: m.opts.ListPort, Proto: "UDP", For: "console server list"})
		if m.opts.DNSPort > 0 {
			want = append(want, Port{Port: m.opts.DNSPort, Proto: "UDP", For: "console DNS"},
				Port{Port: m.opts.DNSPort, Proto: "TCP", For: "console DNS"})
		}
	}

	// The home's internet address.
	var routerIP netip.Addr
	if set.UPnP && set.Enabled || len(m.mapped) > 0 {
		m.findGateway(ctx, &st)
	}
	if m.gw != nil && set.Enabled {
		ip, err := m.gw.externalIP(ctx)
		switch {
		case err != nil && isGone(err):
			m.gw = nil // look for the router again next time
		case err == nil && ip.Is4() && !ip.IsUnspecified():
			routerIP = ip
		}
	}
	// When the router's address is a real internet address that differs
	// from the last lookup, the address has probably just changed: look it
	// up again now rather than wait.
	force := routerIP.IsValid() && !isShared(routerIP) && m.lookedUp.IsValid() && routerIP != m.lookedUp
	public, from := m.publicIP(ctx, set.Enabled, force, &st)
	if !public.IsValid() && routerIP.IsValid() {
		public, from = routerIP, "router"
	}
	if public.IsValid() {
		st.PublicIP, st.PublicIPFrom = public.String(), from
		// A router that isn't on the internet address itself sits behind
		// another router or the provider's shared address.
		st.Shared = isShared(public) || (routerIP.IsValid() && routerIP != public && from != "setting")
	}

	// What friends type, and where it leads.
	host := set.Address
	if host == "" && public.IsValid() {
		host = public.String()
	}
	st.Address = host
	addrIP := public
	if set.Address != "" {
		if ip, err := netip.ParseAddr(set.Address); err == nil {
			addrIP = ip
		} else if set.Enabled {
			rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
			ips, err := net.DefaultResolver.LookupNetIP(rctx, "ip4", set.Address)
			rcancel()
			switch {
			case err != nil || len(ips) == 0:
				st.AddressWarning = set.Address + " doesn't lead anywhere yet (the name can't be looked up)."
				addrIP = public
			default:
				addrIP = ips[0].Unmap()
			}
		}
		if addrIP.IsValid() && isShared(addrIP) {
			// Some homes look their own name up as the house address
			// (a local DNS record). From outside it leads to the internet
			// address, so there's nothing to warn about or pass on.
			addrIP = public
		}
		if addrIP.IsValid() {
			st.AddressIP = addrIP.String()
		}
		if st.AddressWarning == "" && addrIP.IsValid() && public.IsValid() && addrIP != public {
			st.AddressWarning = fmt.Sprintf("%s leads to %s, but this home's internet address is %s. Update the name (or its dynamic DNS) to point here.", set.Address, addrIP, public)
		}
	}

	// The router's forwards.
	if set.UPnP && set.Enabled {
		m.applyForwards(ctx, want, &st)
	} else {
		m.applyForwards(ctx, nil, &st)
		for _, p := range want {
			st.Ports = append(st.Ports, PortState{Port: p})
		}
	}

	info := Info{Enabled: set.Enabled && host != "", Host: host, IP: addrIP, Consoles: set.Enabled && set.Consoles, open: open}
	if !info.IP.IsValid() || isShared(info.IP) {
		info.IP = public
	}
	m.mu.Lock()
	m.status = st
	m.info = info
	m.mu.Unlock()
}

// findGateway looks for the router once, and again after it stops answering.
func (m *Manager) findGateway(ctx context.Context, st *Status) {
	if m.gw == nil {
		g, err := discover(ctx, m.opts.LocalIP, m.opts.GatewayURL)
		if err != nil {
			st.RouterError = err.Error()
			return
		}
		m.gw = g
		m.log.Info("found the router", "name", g.name, "at", g.location)
	}
	st.Router = m.gw.name
	if st.Router == "" {
		st.Router = "your router"
	}
}

// publicIP looks up the home's address on the internet, at most every ten
// minutes (or uses the one set by hand).
func (m *Manager) publicIP(ctx context.Context, enabled, force bool, st *Status) (netip.Addr, string) {
	if m.opts.StaticIP.IsValid() {
		return m.opts.StaticIP, "setting"
	}
	if !enabled || len(m.opts.LookupURLs) == 0 {
		return netip.Addr{}, ""
	}
	if force || time.Since(m.lookedUpAt) > 10*time.Minute || !m.lookedUp.IsValid() && time.Since(m.lookedUpAt) > time.Minute {
		m.lookedUpAt = time.Now()
		ip, err := lookupIP(ctx, m.opts.LookupURLs)
		if err != nil {
			m.lookupErr = err.Error()
		} else {
			m.lookedUp, m.lookupErr = ip, ""
		}
	}
	st.PublicIPError = m.lookupErr
	if m.lookedUp.IsValid() {
		return m.lookedUp, "internet"
	}
	return netip.Addr{}, ""
}

func lookupIP(ctx context.Context, urls []string) (netip.Addr, error) {
	var last error
	for _, u := range urls {
		rctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		req, err := http.NewRequestWithContext(rctx, http.MethodGet, u, nil)
		if err != nil {
			cancel()
			last = err
			continue
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			last = err
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 256))
		res.Body.Close()
		cancel()
		ip, err := netip.ParseAddr(strings.TrimSpace(string(b)))
		if res.StatusCode == http.StatusOK && err == nil && ip.Is4() {
			return ip, nil
		}
		last = fmt.Errorf("%s gave an odd answer", u)
	}
	return netip.Addr{}, fmt.Errorf("couldn't look up this home's internet address: %v", last)
}

// renewAfter is when a forward with a lease is made again.
const renewAfter = 20 * time.Minute

// applyForwards opens the wanted forwards on the router and closes the ones
// this panel made that aren't wanted any more.
func (m *Manager) applyForwards(ctx context.Context, want []Port, st *Status) {
	wanted := map[string]bool{}
	for _, p := range want {
		wanted[key(p)] = true
	}
	changed := false
	local := m.opts.LocalIP.String()
	if m.gw != nil {
		for k, x := range m.mapped {
			if wanted[k] {
				continue
			}
			if err := m.closeForward(ctx, x); err != nil {
				m.log.Warn("couldn't close a port forward", "port", k, "error", err)
				continue
			}
			m.log.Info("closed a port forward", "port", k, "for", x.For)
			delete(m.mapped, k)
			changed = true
		}
	}
	for _, p := range want {
		ps := PortState{Port: p}
		x, had := m.mapped[key(p)]
		switch {
		case m.gw == nil:
			ps.Error = st.RouterError
		// Forwards are made again every so often, permanent ones too: a
		// router that restarts may forget them.
		case had && x.For == p.For && x.Client == local && time.Since(x.At) < renewAfter:
			ps.Opened = true
		default:
			permanent, err := m.gw.addMapping(ctx, p, m.opts.LocalIP)
			if err != nil {
				ps.Error = err.Error()
				m.log.Warn("the router didn't open a port", "port", key(p), "for", p.For, "error", err)
				var ue *upnpError
				if errors.As(err, &ue) && ue.Code == 718 && had {
					// Another device has it now: it isn't ours to close.
					delete(m.mapped, key(p))
					changed = true
				}
				if isGone(err) {
					m.gw = nil // look for the router again next time
				}
				break
			}
			if !had {
				m.log.Info("opened a port forward", "port", key(p), "for", p.For)
			}
			m.mapped[key(p)] = mapping{Port: p, At: time.Now(), Permanent: permanent, Client: local}
			ps.Opened = true
			changed = true
		}
		st.Ports = append(st.Ports, ps)
	}
	if changed {
		m.saveMappings()
	}
}

// closeForward removes a forward this panel made, but only if the router
// still sends it here: after a restart, another device may have taken the
// port.
func (m *Manager) closeForward(ctx context.Context, x mapping) error {
	if m.gw == nil {
		return errors.New("the router isn't answering")
	}
	to, err := m.gw.mappedTo(ctx, x.Port)
	if err != nil {
		if isGone(err) {
			m.gw = nil
		}
		return err
	}
	if to == "" || to != m.opts.LocalIP.String() {
		return nil // gone already, or someone else's now
	}
	err = m.gw.deleteMapping(ctx, x.Port)
	if err != nil && isGone(err) {
		m.gw = nil
	}
	return err
}

// isGone reports an error from the router not answering (rather than
// refusing), so it's looked for again.
func isGone(err error) bool {
	var ue *upnpError
	return !errors.As(err, &ue)
}

func (m *Manager) saveMappings() {
	list := []mapping{}
	for _, x := range m.mapped {
		list = append(list, x)
	}
	sort.Slice(list, func(i, j int) bool { return key(list[i].Port) < key(list[j].Port) })
	b, _ := json.MarshalIndent(list, "", "  ")
	if err := writeFile(m.mappingsPath(), b); err != nil {
		m.log.Warn("couldn't save the list of port forwards", "error", err)
	}
}

func writeFile(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
