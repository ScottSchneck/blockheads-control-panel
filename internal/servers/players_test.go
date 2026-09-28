package servers

import (
	"strings"
	"testing"
	"time"
)

func consoleHas(s *Server, text string) bool {
	backlog, _, stop := s.Subscribe()
	stop()
	for _, l := range backlog {
		if strings.Contains(l, text) {
			return true
		}
	}
	return false
}

func waitConsole(t *testing.T, s *Server, text string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if consoleHas(s, text) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	backlog, _, stop := s.Subscribe()
	stop()
	t.Fatalf("console never showed %q:\n%s", text, strings.Join(backlog, "\n"))
}

func mustPlayers(t *testing.T, s *Server) PlayersView {
	t.Helper()
	v, err := s.Players()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func hasName(list []ListedName, name string) (ListedName, bool) {
	for _, l := range list {
		if strings.EqualFold(l.Name, name) {
			return l, true
		}
	}
	return ListedName{}, false
}

func TestPlayers(t *testing.T) {
	m := newTestManager(t, newFakeMojang(t, "1.0.0.1"), 2*time.Second)
	if err := m.SetOwnerGamertag("Kemikal  Halo "); err != nil {
		t.Fatal(err)
	}
	if got := m.Settings().OwnerGamertag; got != "Kemikal Halo" {
		t.Fatalf("owner %q", got)
	}
	if err := m.SetOwnerGamertag(`bad"name`); err == nil {
		t.Error("quotes should be refused")
	}

	// The owner is on the allowlist of a new server and queued as operator.
	s := createRunning(t, m, "Family")
	v := mustPlayers(t, s)
	if !v.AllowlistEnabled {
		t.Error("allowlist should read as on")
	}
	if _, ok := hasName(v.Allowlist, "Kemikal Halo"); !ok {
		t.Errorf("owner not on the allowlist: %+v", v.Allowlist)
	}
	if op, ok := hasName(v.Operators, "Kemikal Halo"); !ok || !op.Pending {
		t.Errorf("owner should be a pending operator: %+v", v.Operators)
	}

	// Their first join applies it.
	s.Command("join Kemikal Halo")
	waitConsole(t, s, "is now an operator")
	waitConsole(t, s, `got: op "Kemikal Halo"`)
	v = mustPlayers(t, s)
	if op, ok := hasName(v.Operators, "Kemikal Halo"); !ok || op.Pending || op.XUID != "2535400000000000" {
		t.Errorf("owner should be an operator now: %+v", v.Operators)
	}
	if len(v.Online) != 1 {
		t.Errorf("online: %+v", v.Online)
	}
	if m.people.xuidFor("kemikal halo") != "2535400000000000" {
		t.Error("the panel didn't remember their Xbox ID")
	}
	if e, _ := hasName(v.Allowlist, "Kemikal Halo"); e.XUID == "" {
		t.Error("allowlist should show a known Xbox ID (so the page doesn't say \"hasn't joined yet\")")
	}

	// Add and remove on the allowlist, applied live.
	if err := s.AllowlistAdd("Steve", ""); err != nil {
		t.Fatal(err)
	}
	waitConsole(t, s, "got: allowlist reload")
	if _, ok := hasName(mustPlayers(t, s).Allowlist, "Steve"); !ok {
		t.Error("Steve not added")
	}
	if err := s.AllowlistAdd("steve", ""); err != nil {
		t.Fatal(err)
	}
	if n := len(mustPlayers(t, s).Allowlist); n != 2 {
		t.Errorf("adding twice should not duplicate: %d entries", n)
	}
	if err := s.AllowlistRemove("STEVE"); err != nil {
		t.Fatal(err)
	}
	if _, ok := hasName(mustPlayers(t, s).Allowlist, "Steve"); ok {
		t.Error("Steve not removed")
	}
	if err := s.AllowlistRemove("Nobody"); err == nil {
		t.Error("removing someone not listed should say so")
	}
	if err := s.AllowlistAdd(`x"; stop`, ""); err == nil {
		t.Error("names with quotes must be refused")
	}

	// Operators for someone who never joined are queued, and can be undone.
	pending, err := s.OpAdd("Alex")
	if err != nil || !pending {
		t.Fatalf("OpAdd Alex: pending=%v err=%v", pending, err)
	}
	if err := s.OpRemove("alex"); err != nil {
		t.Fatal(err)
	}
	if _, ok := hasName(mustPlayers(t, s).Operators, "Alex"); ok {
		t.Error("Alex still queued")
	}
	// Removing a real operator by gamertag.
	if err := s.OpRemove("Kemikal Halo"); err != nil {
		t.Fatal(err)
	}
	waitConsole(t, s, `got: deop "Kemikal Halo"`)
	if _, ok := hasName(mustPlayers(t, s).Operators, "Kemikal Halo"); ok {
		t.Error("still an operator")
	}
	// And making someone who's online an operator applies straight away.
	if pending, err := s.OpAdd("Kemikal Halo"); err != nil || pending {
		t.Fatalf("OpAdd online player: pending=%v err=%v", pending, err)
	}

	// Someone the menu sends here who isn't on the allowlist.
	m.NoteMenuPick(s.Status().Port, "Ava", "2535400000000999", false)
	waitFor(t, s, "attempt recorded", func(Status) bool { return len(mustPlayers(t, s).Attempts) == 1 })
	a := mustPlayers(t, s).Attempts[0]
	if a.Name != "Ava" || a.XUID != "2535400000000999" || a.Verified {
		t.Errorf("attempt %+v", a)
	}
	// People on the allowlist, and picks for other ports, aren't recorded.
	m.NoteMenuPick(s.Status().Port, "Kemikal Halo", "", true)
	m.NoteMenuPick(19999, "Someone", "", true)
	time.Sleep(100 * time.Millisecond)
	if n := len(mustPlayers(t, s).Attempts); n != 1 {
		t.Errorf("attempts: %d", n)
	}
	// Allowing clears the attempt. The unchecked Xbox ID isn't used.
	if err := s.AllowlistAdd("Ava", s.AttemptXUID("Ava")); err != nil {
		t.Fatal(err)
	}
	v = mustPlayers(t, s)
	if len(v.Attempts) != 0 {
		t.Error("attempt not cleared")
	}
	if e, _ := hasName(v.Allowlist, "Ava"); e.XUID != "" {
		t.Errorf("unchecked Xbox ID was written: %+v", e)
	}

	// With the allowlist off, nobody is "turned away".
	if err := s.SetAllowlistEnabled(false); err != nil {
		t.Fatal(err)
	}
	waitConsole(t, s, "got: allowlist off")
	if mustPlayers(t, s).AllowlistEnabled {
		t.Error("allowlist still on")
	}
	m.NoteMenuPick(s.Status().Port, "Bob", "", false)
	time.Sleep(100 * time.Millisecond)
	if n := len(mustPlayers(t, s).Attempts); n != 0 {
		t.Errorf("attempt recorded with the allowlist off")
	}

	// Kick and messages.
	if err := s.Kick("Kemikal Halo", "bed time"); err != nil {
		t.Fatal(err)
	}
	waitConsole(t, s, `got: kick "Kemikal Halo" bed time`)
	if err := s.Message("", "Dinner!"); err != nil {
		t.Fatal(err)
	}
	waitConsole(t, s, "got: say Dinner!")
	if err := s.Message("Kemikal Halo", "hi\nthere"); err != nil {
		t.Fatal(err)
	}
	waitConsole(t, s, `got: tell "Kemikal Halo" hi there`)

	// Editing while stopped works too; it's picked up at the next start.
	s.Stop()
	waitFor(t, s, "stopped", state(StateStopped))
	if err := s.AllowlistAdd("Offline Friend", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := hasName(mustPlayers(t, s).Allowlist, "Offline Friend"); !ok {
		t.Error("not added while stopped")
	}
}

func TestPeopleOnlyKeepCheckedIDs(t *testing.T) {
	p := newPeople(t.TempDir())
	p.seen("Ava", "111", "Family", true)
	p.seen("Ava", "999", "", false) // an unchecked console claims another ID
	if got := p.xuidFor("ava"); got != "111" {
		t.Errorf("checked ID replaced: %s", got)
	}
	p.seen("Owner", "666", "", false)
	if got := p.xuidFor("owner"); got != "" {
		t.Errorf("unchecked ID stored: %s", got)
	}
	if p.nameFor("666") != "" {
		t.Error("unchecked ID resolves to a name")
	}
}

// A console that isn't signed in can't make itself an operator by claiming
// the owner's gamertag in the menu.
func TestSpoofedMenuPickDoesNotGrantOperator(t *testing.T) {
	m := newTestManager(t, newFakeMojang(t, "1.0.0.1"), 2*time.Second)
	first := createRunning(t, m, "First")
	m.NoteMenuPick(first.Status().Port, "Owner Tag", "666", false)
	m.SetOwnerGamertag("Owner Tag")
	s := createRunning(t, m, "Second")
	v := mustPlayers(t, s)
	for _, o := range v.Operators {
		if o.XUID == "666" {
			t.Fatalf("spoofed ID became operator: %+v", v.Operators)
		}
	}
	if op, ok := hasName(v.Operators, "Owner Tag"); !ok || !op.Pending {
		t.Errorf("owner should wait for their real first join: %+v", v.Operators)
	}
	if e, _ := hasName(v.Allowlist, "Owner Tag"); e.XUID == "666" {
		t.Error("spoofed ID written to the allowlist")
	}
}
