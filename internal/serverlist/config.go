package serverlist

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// Config holds every setting for the console server list. LoadConfig reads it
// from environment variables so a container can be configured from its
// template without editing files; the panel will later fill it from its own
// settings instead.
type Config struct {
	// ListIP is the address the built-in DNS hands to devices on the home
	// network for the featured-server names. Normally this container's own IP.
	ListIP netip.Addr
	// PublicIP is handed to devices outside the home network, and advertised
	// in WebRTC candidates. Optional; empty means home-only.
	PublicIP netip.Addr

	DNSEnabled     bool
	DNSPort        int
	DNSUpstream    string
	DNSAnswerWorld bool // answer the redirect names for internet clients (friends mode)
	RedirectNames  []string

	ListPort int
	// Connection is what the server list offers consoles:
	//   raknet    - the older connection type only (what works today)
	//   nethernet - the newer connection type only
	//   both      - both; NetherNetNames can limit NetherNet to some names
	Connection string
	// NetherNetNames limits NetherNet to these featured-server names when
	// Connection is "both". Consoles joining any other name are turned away
	// from NetherNet and fall back to RakNet. Empty means every name.
	NetherNetNames map[string]bool
	// Signaling is how the NetherNet join endpoint answers: auto (HTTPS and
	// plain HTTP on the same port), http, or tls.
	Signaling string
	// ExtraSignalingPorts are more TCP ports that serve the NetherNet join
	// endpoint and log every connection, to see where consoles try next.
	ExtraSignalingPorts []int
	RakNet              bool
	NetherNet           bool

	RTCPortMin uint16
	RTCPortMax uint16

	// Load settings control how the empty menu world is built, to find what
	// makes consoles finish loading quickly. LOAD_PRESET=bedrockconnect copies
	// BedrockConnect's choices; the individual settings override the preset.
	LoadGameMode   int32   // 0 survival, 1 creative, 6 spectator
	LoadSpawnY     float32 // height the player appears at
	LoadChunksAt   string  // "radius": after the view distance is agreed; "startgame": right after the game starts
	LoadChunkStyle string  // "standard" or "bedrockconnect"
	LoadChunkRange int32   // chunks sent in each direction from the spawn

	ServersFile string
	DataDir     string
	AuthOff     bool // testing only: skip Xbox sign-in checks
	// RequireSignIn turns away consoles whose Xbox sign-in can't be verified.
	// Off by default: the game server does its own checks.
	RequireSignIn bool
	MenuTitle     string
	LogLevel      slog.Level
}

// Server is one entry in the console server list. The JSON shape matches
// BedrockConnect's custom_servers.json so an existing file can be reused.
type Server struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Port    uint16 `json:"port"`
	IconURL string `json:"iconUrl,omitempty"`
}

// defaultRedirectNames are the featured-server hostnames consoles look up.
// Taken from BedrockConnect's "Using your own DNS server" wiki page.
var defaultRedirectNames = []string{
	"geo.hivebedrock.network",
	"hivebedrock.network",
	"play.inpvp.net",
	"mco.lbsg.net",
	"play.galaxite.net",
	"play.enchanted.gg",
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envBool(key string, def bool) bool {
	v := strings.ToLower(env(key, strconv.FormatBool(def)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func envInt(key string, def int) (int, error) {
	v := env(key, strconv.Itoa(def))
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number, got %q", key, v)
	}
	return n, nil
}

// LoadConfig reads the settings from environment variables. See the README
// for the list.
func LoadConfig() (*Config, error) {
	c := &Config{
		DNSUpstream:    env("DNS_UPSTREAM", "1.1.1.1:53"),
		Signaling:      strings.ToLower(env("SIGNALING", "auto")),
		ServersFile:    env("SERVERS_FILE", "/config/servers.json"),
		DataDir:        env("DATA_DIR", "/data"),
		MenuTitle:      env("MENU_TITLE", "Pick a server"),
		DNSEnabled:     envBool("DNS_ENABLED", true),
		DNSAnswerWorld: envBool("DNS_ANSWER_INTERNET", false),
		AuthOff:        envBool("AUTH_OFF", false),
		RequireSignIn:  envBool("REQUIRE_SIGN_IN", false),
	}

	listIP := env("LIST_IP", "")
	if listIP == "" {
		return nil, fmt.Errorf("LIST_IP is required: set it to this container's IP on your home network (for example 10.0.1.116)")
	}
	ip, err := netip.ParseAddr(listIP)
	if err != nil || !ip.Is4() {
		return nil, fmt.Errorf("LIST_IP must be an IPv4 address, got %q", listIP)
	}
	c.ListIP = ip

	if pub := env("PUBLIC_IP", ""); pub != "" {
		ip, err := netip.ParseAddr(pub)
		if err != nil || !ip.Is4() {
			return nil, fmt.Errorf("PUBLIC_IP must be an IPv4 address, got %q", pub)
		}
		c.PublicIP = ip
	}

	if c.DNSPort, err = envInt("DNS_PORT", 53); err != nil {
		return nil, err
	}
	if c.ListPort, err = envInt("LIST_PORT", 19132); err != nil {
		return nil, err
	}
	minP, err := envInt("RTC_PORT_MIN", 19300)
	if err != nil {
		return nil, err
	}
	maxP, err := envInt("RTC_PORT_MAX", 19399)
	if err != nil {
		return nil, err
	}
	if minP < 1024 || maxP > 65535 || minP > maxP {
		return nil, fmt.Errorf("RTC_PORT_MIN/RTC_PORT_MAX must be a range between 1024 and 65535")
	}
	c.RTCPortMin, c.RTCPortMax = uint16(minP), uint16(maxP)

	// CONNECTION picks what consoles are offered. The first build used
	// SIGNALING=off and RAKNET=false for this, so those still work.
	c.Connection = strings.ToLower(env("CONNECTION", ""))
	if c.Connection == "" {
		switch {
		case c.Signaling == "off":
			c.Connection = "raknet"
		case !envBool("RAKNET", true):
			c.Connection = "nethernet"
		case os.Getenv("SIGNALING") != "":
			c.Connection = "both"
		default:
			c.Connection = "raknet"
		}
	}
	switch c.Connection {
	case "raknet":
		c.RakNet = true
	case "nethernet":
		c.NetherNet = true
	case "both":
		c.RakNet, c.NetherNet = true, true
	default:
		return nil, fmt.Errorf("CONNECTION must be raknet, nethernet or both, got %q", c.Connection)
	}
	if c.Signaling == "off" {
		c.Signaling = "auto"
	}
	switch c.Signaling {
	case "auto", "http", "tls":
	default:
		return nil, fmt.Errorf("SIGNALING must be auto, http or tls, got %q", c.Signaling)
	}

	c.NetherNetNames = map[string]bool{}
	for _, n := range splitList(env("NETHERNET_NAMES", "")) {
		c.NetherNetNames[strings.ToLower(n)] = true
	}
	if len(c.NetherNetNames) > 0 && c.Connection != "both" {
		slog.Warn("NETHERNET_NAMES only applies when CONNECTION=both; ignoring it")
		c.NetherNetNames = map[string]bool{}
	}

	extra := env("EXTRA_SIGNALING_PORTS", "80,443")
	if v := strings.ToLower(extra); v == "none" || v == "off" {
		extra = "" // "none" turns the extra ports off; an empty value means the default
	}
	for _, p := range splitList(extra) {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("EXTRA_SIGNALING_PORTS must be port numbers separated by commas, got %q", p)
		}
		if n != c.ListPort {
			c.ExtraSignalingPorts = append(c.ExtraSignalingPorts, n)
		}
	}

	c.RedirectNames = defaultRedirectNames
	if names := splitList(env("REDIRECT_NAMES", "")); len(names) > 0 {
		c.RedirectNames = names
	}

	if err := loadLoadSettings(c); err != nil {
		return nil, err
	}

	switch strings.ToLower(env("LOG_LEVEL", "info")) {
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "warn":
		c.LogLevel = slog.LevelWarn
	default:
		c.LogLevel = slog.LevelInfo
	}
	return c, nil
}

func loadLoadSettings(c *Config) error {
	preset := strings.ToLower(env("LOAD_PRESET", "standard"))
	switch preset {
	case "standard":
		c.LoadGameMode, c.LoadSpawnY, c.LoadChunksAt, c.LoadChunkStyle, c.LoadChunkRange = 6, 100, "radius", "standard", 4
	case "bedrockconnect":
		c.LoadGameMode, c.LoadSpawnY, c.LoadChunksAt, c.LoadChunkStyle, c.LoadChunkRange = 0, 0, "startgame", "bedrockconnect", 3
	default:
		return fmt.Errorf("LOAD_PRESET must be standard or bedrockconnect, got %q", preset)
	}
	switch strings.ToLower(env("LOAD_GAMEMODE", "")) {
	case "":
	case "survival":
		c.LoadGameMode = 0
	case "creative":
		c.LoadGameMode = 1
	case "spectator":
		c.LoadGameMode = 6
	default:
		return fmt.Errorf("LOAD_GAMEMODE must be survival, creative or spectator")
	}
	if v := env("LOAD_SPAWN_Y", ""); v != "" {
		f, err := strconv.ParseFloat(v, 32)
		if err != nil {
			return fmt.Errorf("LOAD_SPAWN_Y must be a number, got %q", v)
		}
		c.LoadSpawnY = float32(f)
	}
	if v := strings.ToLower(env("LOAD_CHUNKS_AT", "")); v != "" {
		if v != "radius" && v != "startgame" {
			return fmt.Errorf("LOAD_CHUNKS_AT must be radius or startgame, got %q", v)
		}
		c.LoadChunksAt = v
	}
	if v := strings.ToLower(env("LOAD_CHUNK_STYLE", "")); v != "" {
		if v != "standard" && v != "bedrockconnect" {
			return fmt.Errorf("LOAD_CHUNK_STYLE must be standard or bedrockconnect, got %q", v)
		}
		c.LoadChunkStyle = v
	}
	if v := env("LOAD_CHUNK_RANGE", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 8 {
			return fmt.Errorf("LOAD_CHUNK_RANGE must be a number from 1 to 8, got %q", v)
		}
		c.LoadChunkRange = int32(n)
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// allowsNetherNet reports whether a console joining the given name (a Host
// header or TLS server name, with or without a port) should be offered
// NetherNet.
func (c *Config) allowsNetherNet(host string) bool {
	if len(c.NetherNetNames) == 0 {
		return true
	}
	h := strings.ToLower(host)
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	return c.NetherNetNames[strings.TrimSuffix(h, ".")]
}

// loadServers reads the server list fresh each time a console opens the menu,
// so edits to the file show up without restarting the container.
func loadServers(path string) ([]Server, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var servers []Server
	if err := json.Unmarshal(b, &servers); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	valid := servers[:0]
	for _, s := range servers {
		if s.Name == "" || s.Address == "" || s.Port == 0 {
			slog.Warn("skipping server entry with a missing name, address or port", "entry", s)
			continue
		}
		valid = append(valid, s)
	}
	return valid, nil
}
