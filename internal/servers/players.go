package servers

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Players: the allowlist, operators, who's online and who tried to join.
//
// The allowlist and operators live in the server's own allowlist.json and
// permissions.json. The panel edits those files and, when the server is
// running, tells it to reload them, so changes apply without a restart.
//
// Bedrock stores operators by Xbox ID (XUID), which it only learns when a
// player joins. Making someone an operator before they've ever joined is
// queued (Meta.PendingOps) and applied the first time they connect.

// PlayersView is everything the Players page shows for one server.
type PlayersView struct {
	Online           []Player      `json:"online"`
	AllowlistEnabled bool          `json:"allowlistEnabled"`
	Allowlist        []ListedName  `json:"allowlist"`
	Operators        []ListedName  `json:"operators"`
	Attempts         []JoinAttempt `json:"attempts"`
	Running          bool          `json:"running"`
}

// ListedName is a player on the allowlist or an operator.
type ListedName struct {
	Name    string `json:"name"`
	XUID    string `json:"xuid,omitempty"`
	Pending bool   `json:"pending,omitempty"` // operator waiting for the player's first join
}

// JoinAttempt is someone who tried to join but isn't on the allowlist.
type JoinAttempt struct {
	Name     string    `json:"name"`
	XUID     string    `json:"xuid,omitempty"`
	At       time.Time `json:"at"`
	Verified bool      `json:"verified"` // their Xbox sign-in was checked
}

type allowEntry struct {
	IgnoresPlayerLimit bool   `json:"ignoresPlayerLimit"`
	Name               string `json:"name"`
	XUID               string `json:"xuid,omitempty"`
}

type permEntry struct {
	Permission string `json:"permission"`
	XUID       string `json:"xuid"`
}

const maxAttempts = 20

// Gamertags are letters, digits and spaces; allow a little more (PlayStation
// and Switch names, the #1234 suffix) but never quotes, which would break the
// console commands the panel sends.
var gamertagPattern = regexp.MustCompile(`^[\p{L}\p{N} _.\-#]{1,32}$`)

// ErrBadGamertag is returned for names that can't be a gamertag.
var ErrBadGamertag = errors.New("that doesn't look like a gamertag (letters, numbers and spaces, up to 32)")

func cleanGamertag(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if !gamertagPattern.MatchString(name) {
		return "", ErrBadGamertag
	}
	return name, nil
}

func (s *Server) allowlistPath() string   { return filepath.Join(s.dir(), "allowlist.json") }
func (s *Server) permissionsPath() string { return filepath.Join(s.dir(), "permissions.json") }

func readJSONList[T any](path string) ([]T, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, nil
	}
	var out []T
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("%s isn't valid JSON: %w", filepath.Base(path), err)
	}
	return out, nil
}

func writeJSONList[T any](path string, list []T) error {
	if list == nil {
		list = []T{}
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// reloadIfRunning asks a running server to re-read a file it just had
// changed under it.
func (s *Server) reloadIfRunning(commands ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil || s.stopping {
		return
	}
	for _, c := range commands {
		s.sendLocked(c)
	}
}

func (s *Server) onlineXUID(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.players {
		if strings.EqualFold(p.Name, name) {
			return p.XUID, true
		}
	}
	return "", false
}

// checksSignIn reports whether the server checks Xbox sign-in (online-mode,
// on unless someone turned it off by hand).
func (s *Server) checksSignIn() bool {
	p, err := readProperties(filepath.Join(s.dir(), "server.properties"))
	if err != nil {
		return false
	}
	v, ok := p.get("online-mode")
	return !ok || !strings.EqualFold(v, "false")
}

// allowlistEnabled reads allow-list (or the older white-list) from
// server.properties.
func (s *Server) allowlistEnabled() bool {
	p, err := readProperties(filepath.Join(s.dir(), "server.properties"))
	if err != nil {
		return false
	}
	for _, key := range []string{"allow-list", "white-list"} {
		if v, ok := p.get(key); ok {
			return strings.EqualFold(v, "true")
		}
	}
	return false
}

// Players returns the Players page for this server.
func (s *Server) Players() (PlayersView, error) {
	s.filesMu.Lock()
	defer s.filesMu.Unlock()
	// Empty lists, never nil: the page expects [] rather than null.
	v := PlayersView{
		AllowlistEnabled: s.allowlistEnabled(),
		Online:           []Player{},
		Allowlist:        []ListedName{},
		Operators:        []ListedName{},
		Attempts:         []JoinAttempt{},
	}

	allow, err := readJSONList[allowEntry](s.allowlistPath())
	if err != nil {
		return v, err
	}
	for _, a := range allow {
		xuid := a.XUID
		if xuid == "" { // Bedrock fills it in on their next join; the panel may already know it
			xuid = s.m.people.xuidFor(a.Name)
		}
		v.Allowlist = append(v.Allowlist, ListedName{Name: a.Name, XUID: xuid})
	}
	sort.Slice(v.Allowlist, func(i, j int) bool {
		return strings.ToLower(v.Allowlist[i].Name) < strings.ToLower(v.Allowlist[j].Name)
	})

	perms, err := readJSONList[permEntry](s.permissionsPath())
	if err != nil {
		return v, err
	}
	for _, p := range perms {
		if p.Permission != "operator" {
			continue
		}
		name := s.m.people.nameFor(p.XUID)
		if name == "" {
			for _, a := range allow {
				if a.XUID == p.XUID {
					name = a.Name
				}
			}
		}
		if name == "" {
			name = "Xbox ID " + p.XUID
		}
		v.Operators = append(v.Operators, ListedName{Name: name, XUID: p.XUID})
	}

	st := s.Status()
	if st.Players != nil {
		v.Online = st.Players
	}
	v.Running = st.State == StateRunning
	s.mu.Lock()
	for _, n := range s.meta.PendingOps {
		v.Operators = append(v.Operators, ListedName{Name: n, Pending: true})
	}
	v.Attempts = append([]JoinAttempt{}, s.attempts...)
	s.mu.Unlock()
	sort.Slice(v.Attempts, func(i, j int) bool { return v.Attempts[i].At.After(v.Attempts[j].At) })
	return v, nil
}

// AllowlistAdd adds a player to the allowlist. xuid may be empty; Bedrock
// fills it in when they join.
func (s *Server) AllowlistAdd(name, xuid string) error {
	name, err := cleanGamertag(name)
	if err != nil {
		return err
	}
	if xuid == "" {
		xuid = s.m.people.xuidFor(name)
	}
	s.filesMu.Lock()
	allow, err := readJSONList[allowEntry](s.allowlistPath())
	if err == nil {
		found := false
		for i, a := range allow {
			if strings.EqualFold(a.Name, name) {
				found = true
				if a.XUID == "" && xuid != "" {
					allow[i].XUID = xuid
				}
			}
		}
		if !found {
			allow = append(allow, allowEntry{Name: name, XUID: xuid})
		}
		err = writeJSONList(s.allowlistPath(), allow)
	}
	s.filesMu.Unlock()
	if err != nil {
		return err
	}
	s.dismissAttempt(name)
	s.reloadIfRunning("allowlist reload")
	s.say(name + " was added to the allowlist.")
	return nil
}

// AllowlistRemove takes a player off the allowlist. Someone who's playing
// stays until they leave; use Kick to remove them now.
func (s *Server) AllowlistRemove(name string) error {
	s.filesMu.Lock()
	allow, err := readJSONList[allowEntry](s.allowlistPath())
	removed := false
	if err == nil {
		kept := allow[:0]
		for _, a := range allow {
			if strings.EqualFold(a.Name, name) {
				removed = true
				continue
			}
			kept = append(kept, a)
		}
		if removed {
			err = writeJSONList(s.allowlistPath(), kept)
		}
	}
	s.filesMu.Unlock()
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("%s isn't on the allowlist", name)
	}
	s.reloadIfRunning("allowlist reload")
	s.say(name + " was removed from the allowlist.")
	return nil
}

// SetAllowlistEnabled turns the allowlist on or off.
func (s *Server) SetAllowlistEnabled(on bool) error {
	value := "false"
	command := "allowlist off"
	if on {
		value, command = "true", "allowlist on"
	}
	// server.properties is also rewritten under s.mu when the server starts,
	// so edit it under s.mu too (after filesMu, the usual order).
	s.filesMu.Lock()
	s.mu.Lock()
	path := filepath.Join(s.dir(), "server.properties")
	p, err := readProperties(path)
	if err == nil {
		if !p.setIfPresent("white-list", value) {
			p.set("allow-list", value)
		} else {
			p.setIfPresent("allow-list", value)
		}
		err = p.write(path)
	}
	s.mu.Unlock()
	s.filesMu.Unlock()
	if err != nil {
		return err
	}
	s.reloadIfRunning(command)
	if on {
		s.say("The allowlist is on: only players on it can join.")
	} else {
		s.say("The allowlist is off: anyone who can reach the server can join.")
	}
	return nil
}

// OpAdd makes a player an operator. It returns pending=true if they haven't
// joined yet, in which case it's applied on their first join.
func (s *Server) OpAdd(name string) (pending bool, err error) {
	name, err = cleanGamertag(name)
	if err != nil {
		return false, err
	}
	xuid, online := s.onlineXUID(name)
	if xuid == "" {
		xuid = s.m.people.xuidFor(name)
	}
	if xuid == "" {
		s.mu.Lock()
		already := false
		for _, n := range s.meta.PendingOps {
			already = already || strings.EqualFold(n, name)
		}
		if !already {
			s.meta.PendingOps = append(s.meta.PendingOps, name)
		}
		s.mu.Unlock()
		if err := s.saveMeta(); err != nil {
			return true, err
		}
		s.say(name + " will be made an operator the first time they join.")
		return true, nil
	}
	if err := s.setOperator(xuid, true); err != nil {
		return false, err
	}
	s.removePendingOp(name)
	if online {
		s.reloadIfRunning("permission reload", fmt.Sprintf("op %q", name))
	} else {
		s.reloadIfRunning("permission reload")
	}
	s.say(name + " is now an operator.")
	return false, nil
}

// OpRemove takes operator away. key is a gamertag, or an XUID for operators
// the panel doesn't know by name.
func (s *Server) OpRemove(key string) error {
	if s.removePendingOp(key) {
		s.say(key + " won't be made an operator.")
		return nil
	}
	xuid := key
	name := s.m.people.nameFor(key)
	if !isDigits(key) {
		name = key
		xuid, _ = s.onlineXUID(key)
		if xuid == "" {
			xuid = s.m.people.xuidFor(key)
		}
	}
	if xuid == "" {
		return fmt.Errorf("%s isn't an operator", key)
	}
	if err := s.setOperator(xuid, false); err != nil {
		return err
	}
	cmds := []string{"permission reload"}
	if _, online := s.onlineXUID(name); online && name != "" {
		cmds = append(cmds, fmt.Sprintf("deop %q", name))
	}
	s.reloadIfRunning(cmds...)
	if name == "" {
		name = "Xbox ID " + xuid
	}
	s.say(name + " is no longer an operator.")
	return nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (s *Server) setOperator(xuid string, on bool) error {
	s.filesMu.Lock()
	defer s.filesMu.Unlock()
	perms, err := readJSONList[permEntry](s.permissionsPath())
	if err != nil {
		return err
	}
	kept := perms[:0]
	for _, p := range perms {
		if p.XUID != xuid {
			kept = append(kept, p)
		}
	}
	if on {
		kept = append(kept, permEntry{Permission: "operator", XUID: xuid})
	}
	return writeJSONList(s.permissionsPath(), kept)
}

func (s *Server) removePendingOp(name string) bool {
	s.mu.Lock()
	kept := s.meta.PendingOps[:0]
	removed := false
	for _, n := range s.meta.PendingOps {
		if strings.EqualFold(n, name) {
			removed = true
			continue
		}
		kept = append(kept, n)
	}
	s.meta.PendingOps = kept
	s.mu.Unlock()
	if removed {
		_ = s.saveMeta()
	}
	return removed
}

// onJoin runs (in its own goroutine) when a player connects: it remembers
// their Xbox ID and applies a queued operator.
func (s *Server) onJoin(name, xuid string) {
	// The game server checks Xbox sign-in unless online-mode was turned off.
	s.m.people.seen(name, xuid, s.displayName(), s.checksSignIn())
	s.dismissAttempt(name)
	if xuid == "" {
		return
	}
	// Take them off the queue first, so an Operators "Remove" at the same
	// moment either wins or is too late, never both.
	if !s.removePendingOp(name) {
		return
	}
	if err := s.setOperator(xuid, true); err != nil {
		s.mu.Lock()
		s.meta.PendingOps = append(s.meta.PendingOps, name) // try again next join
		s.mu.Unlock()
		_ = s.saveMeta()
		s.say("Couldn't make " + name + " an operator: " + err.Error())
		return
	}
	s.reloadIfRunning("permission reload", fmt.Sprintf("op %q", name))
	s.say(name + " joined for the first time and is now an operator.")
}

// Kick disconnects a player.
func (s *Server) Kick(name, reason string) error {
	name, err := cleanGamertag(name)
	if err != nil {
		return err
	}
	reason = oneLine(reason, 100)
	cmd := fmt.Sprintf("kick %q", name)
	if reason != "" {
		cmd += " " + reason
	}
	return s.Command(cmd)
}

// Message sends a chat message to one player, or to everyone if name is
// empty.
func (s *Server) Message(name, text string) error {
	text = oneLine(text, 200)
	if text == "" {
		return errors.New("type a message")
	}
	if name == "" {
		return s.Command("say " + text)
	}
	name, err := cleanGamertag(name)
	if err != nil {
		return err
	}
	return s.Command(fmt.Sprintf("tell %q %s", name, text))
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}

// ---- tried to join ----

// noteJoinAttempt records a player the console menu sent here, if they
// aren't on the allowlist.
func (s *Server) noteJoinAttempt(name, xuid string, verified bool) {
	if !s.allowlistEnabled() {
		return
	}
	s.filesMu.Lock()
	allow, err := readJSONList[allowEntry](s.allowlistPath())
	s.filesMu.Unlock()
	if err != nil {
		return
	}
	for _, a := range allow {
		if strings.EqualFold(a.Name, name) || (xuid != "" && a.XUID == xuid) {
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.attempts[:0]
	for _, a := range s.attempts {
		if !strings.EqualFold(a.Name, name) {
			kept = append(kept, a)
		}
	}
	s.attempts = append(kept, JoinAttempt{Name: name, XUID: xuid, At: time.Now(), Verified: verified})
	if len(s.attempts) > maxAttempts {
		s.attempts = s.attempts[len(s.attempts)-maxAttempts:]
	}
	s.addLineLocked("[Panel] " + name + " tried to join but isn't on the allowlist. Allow them on the Players tab.")
}

// DismissAttempt removes someone from the tried-to-join list.
func (s *Server) DismissAttempt(name string) { s.dismissAttempt(name) }

func (s *Server) dismissAttempt(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.attempts[:0]
	for _, a := range s.attempts {
		if !strings.EqualFold(a.Name, name) {
			kept = append(kept, a)
		}
	}
	s.attempts = kept
}

// AttemptXUID returns the Xbox ID a tried-to-join entry came with, if their
// sign-in was checked. An unchecked ID is never used: Bedrock fills in the
// real one when they join.
func (s *Server) AttemptXUID(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.attempts {
		if strings.EqualFold(a.Name, name) && a.Verified {
			return a.XUID
		}
	}
	return ""
}
