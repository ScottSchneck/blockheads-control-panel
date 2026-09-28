package servers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// people remembers every player the panel has seen, so it can turn a
// gamertag into an Xbox ID (which Bedrock needs for operators) and back.
// Stored in <data>/players.json.
type people struct {
	path   string
	mu     sync.Mutex
	saveMu sync.Mutex
	byName map[string]Person // lower-case name
}

// Person is a player the panel has seen.
type Person struct {
	Name       string    `json:"name"`
	XUID       string    `json:"xuid,omitempty"`
	LastSeen   time.Time `json:"lastSeen"`
	LastServer string    `json:"lastServer,omitempty"`
	// Verified is always true: only checked sign-ins are stored.
	Verified bool `json:"verified"`
}

func newPeople(dataDir string) *people {
	p := &people{path: filepath.Join(dataDir, "players.json"), byName: map[string]Person{}}
	if b, err := os.ReadFile(p.path); err == nil {
		var list []Person
		if json.Unmarshal(b, &list) == nil {
			for _, x := range list {
				if x.Verified {
					p.byName[strings.ToLower(x.Name)] = x
				}
			}
		}
	}
	return p
}

// seen records a player whose Xbox sign-in was checked. Unchecked
// identities (some consoles in the menu) are never stored: anyone could claim
// any gamertag and Xbox ID there, and these IDs end up in permissions.json.
func (p *people) seen(name, xuid, server string, verified bool) {
	if name == "" || !verified {
		return
	}
	p.mu.Lock()
	key := strings.ToLower(name)
	old, ok := p.byName[key]
	x := Person{Name: name, XUID: xuid, LastSeen: time.Now().UTC(), LastServer: server, Verified: true}
	if ok {
		if server == "" {
			x.LastServer = old.LastServer
		}
		if xuid == "" {
			x.XUID = old.XUID
		}
	}
	p.byName[key] = x
	p.mu.Unlock()
	go p.save()
}

func (p *people) xuidFor(name string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.byName[strings.ToLower(strings.TrimSpace(name))].XUID
}

func (p *people) nameFor(xuid string) string {
	if xuid == "" {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, x := range p.byName {
		if x.XUID == xuid {
			return x.Name
		}
	}
	return ""
}

func (p *people) save() {
	p.saveMu.Lock()
	defer p.saveMu.Unlock()
	p.mu.Lock()
	list := make([]Person, 0, len(p.byName))
	for _, x := range p.byName {
		list = append(list, x)
	}
	p.mu.Unlock()
	b, _ := json.MarshalIndent(list, "", "  ")
	if err := os.WriteFile(p.path+".tmp", b, 0o644); err == nil {
		_ = os.Rename(p.path+".tmp", p.path)
	}
}

// ---- panel settings ----

// Settings are panel-wide preferences.
type Settings struct {
	// OwnerGamertag is added to the allowlist and made an operator on
	// every new server.
	OwnerGamertag string `json:"ownerGamertag,omitempty"`
}

func (m *Manager) settingsPath() string { return filepath.Join(m.opts.DataDir, "panel-settings.json") }

// Settings returns the panel settings.
func (m *Manager) Settings() Settings {
	m.settingsMu.Lock()
	defer m.settingsMu.Unlock()
	var s Settings
	if b, err := os.ReadFile(m.settingsPath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// SetOwnerGamertag saves the owner's gamertag ("" clears it).
func (m *Manager) SetOwnerGamertag(name string) error {
	if strings.TrimSpace(name) != "" {
		var err error
		if name, err = cleanGamertag(name); err != nil {
			return err
		}
	}
	m.settingsMu.Lock()
	defer m.settingsMu.Unlock()
	var s Settings
	if b, err := os.ReadFile(m.settingsPath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	s.OwnerGamertag = strings.TrimSpace(name)
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := os.WriteFile(m.settingsPath()+".tmp", b, 0o644); err != nil {
		return err
	}
	return os.Rename(m.settingsPath()+".tmp", m.settingsPath())
}

// NoteMenuPick is called when the console menu sends a player to one of the
// panel's servers (identified by port). If that server's allowlist is on and
// they aren't on it, they appear under "Tried to join".
func (m *Manager) NoteMenuPick(port int, name, xuid string, verified bool) {
	if name == "" {
		return
	}
	for _, s := range m.all() {
		s.mu.Lock()
		match := s.meta.Port == port
		s.mu.Unlock()
		if match {
			m.people.seen(name, xuid, "", verified)
			go s.noteJoinAttempt(name, xuid, verified)
			return
		}
	}
}
