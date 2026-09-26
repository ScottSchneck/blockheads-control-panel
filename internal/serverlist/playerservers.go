package serverlist

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Player servers are the servers a player saved from the console menu with
// "Connect to a server". Each player sees only their own, below the house
// servers from servers.json, which only the owner can change.
//
// They're keyed by XUID, which the console reports. PlayStation consoles sign
// their own tokens (see docs/proof-of-concept.md), so the XUID can't always be
// verified. That's acceptable here: the worst someone could do by pretending
// to be another player is see or change that player's saved addresses.

const maxPlayerServers = 30

type playerEntry struct {
	Gamertag string   `json:"gamertag"`
	Servers  []Server `json:"servers"`
}

type playerStore struct {
	mu   sync.Mutex
	path string
}

func newPlayerStore(dataDir string) *playerStore {
	return &playerStore{path: filepath.Join(dataDir, "player-servers.json")}
}

// playerKey identifies a player: their XUID, or their gamertag when the
// console didn't send one.
func playerKey(xuid, gamertag string) string {
	if xuid != "" {
		return xuid
	}
	if gamertag != "" {
		return "name:" + strings.ToLower(gamertag)
	}
	return ""
}

func (p *playerStore) load() (map[string]playerEntry, error) {
	b, err := os.ReadFile(p.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]playerEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]playerEntry{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", p.path, err)
	}
	return m, nil
}

func (p *playerStore) save(m map[string]playerEntry) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p.path) // replace in one step so a crash can't leave half a file
}

// list returns a player's saved servers.
func (p *playerStore) list(key string) ([]Server, error) {
	if key == "" {
		return nil, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	m, err := p.load()
	if err != nil {
		return nil, err
	}
	return m[key].Servers, nil
}

// add saves a server to a player's list, replacing one with the same address
// and port.
func (p *playerStore) add(key, gamertag string, s Server) error {
	if key == "" {
		return errors.New("no player to save for")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	m, err := p.load()
	if err != nil {
		return err
	}
	e := m[key]
	e.Gamertag = gamertag
	kept := e.Servers[:0]
	for _, old := range e.Servers {
		if !strings.EqualFold(old.Address, s.Address) || old.Port != s.Port {
			kept = append(kept, old)
		}
	}
	if len(kept) >= maxPlayerServers {
		return fmt.Errorf("you can save up to %d servers; remove one first", maxPlayerServers)
	}
	e.Servers = append(kept, s)
	m[key] = e
	return p.save(m)
}

// remove deletes a saved server by address and port.
func (p *playerStore) remove(key string, s Server) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	m, err := p.load()
	if err != nil {
		return err
	}
	e, ok := m[key]
	if !ok {
		return nil
	}
	kept := e.Servers[:0]
	for _, old := range e.Servers {
		if !strings.EqualFold(old.Address, s.Address) || old.Port != s.Port {
			kept = append(kept, old)
		}
	}
	e.Servers = kept
	if len(kept) == 0 {
		delete(m, key)
	} else {
		m[key] = e
	}
	return p.save(m)
}

var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)

// parsePlayerServer checks what a player typed into the "Connect to a server"
// form and returns the server, or a message to show them.
func parsePlayerServer(address, port, name string, redirectNames []string) (Server, string) {
	address = strings.TrimSuffix(strings.TrimSpace(address), ".")
	port = strings.TrimSpace(port)
	name = strings.TrimSpace(name)

	if address == "" {
		return Server{}, "Type the server's address."
	}
	// People often paste "host:port" into the address box.
	if h, p, err := net.SplitHostPort(address); err == nil && p != "" {
		address = h
		if port == "" || port == "19132" {
			port = p
		}
	}
	if len(address) > 253 || (net.ParseIP(address) == nil && !hostnamePattern.MatchString(address)) {
		return Server{}, "That address doesn't look right. Use something like play.example.com or 192.168.1.50."
	}
	for _, r := range redirectNames {
		if strings.EqualFold(address, r) {
			return Server{}, "That address leads back to this list. Use the server's own address."
		}
	}
	if port == "" {
		port = "19132"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return Server{}, "The port must be a number from 1 to 65535. Most servers use 19132."
	}
	if len([]rune(name)) > 32 {
		name = string([]rune(name)[:32])
	}
	if name == "" {
		name = address
		if n != 19132 {
			name += ":" + strconv.Itoa(n)
		}
	}
	return Server{Name: name, Address: address, Port: uint16(n)}, ""
}
