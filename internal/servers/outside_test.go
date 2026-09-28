package servers

import (
	"strings"
	"testing"
	"time"
)

func TestOpenToFriendsOutsideNeedsTheAllowlist(t *testing.T) {
	m := newTestManager(t, newFakeMojang(t, "1.0.0.1"), time.Second)
	s := createRunning(t, m, "Family")
	// A new server's allowlist is on, and Xbox sign-in is checked.
	if p := s.OutsideProblem(); p != "" {
		t.Fatalf("problem with a new server: %s", p)
	}
	if err := s.SetOutside(true); err != nil {
		t.Fatal(err)
	}
	if !s.Status().Outside {
		t.Error("status doesn't say it's open")
	}
	// While it's open, nothing may turn off the allowlist or sign-in checks.
	if err := s.SetAllowlistEnabled(false); err == nil {
		t.Error("allowlist turned off while open")
	}
	if _, err := s.SaveSettings(map[string]string{"online-mode": "false"}); err == nil {
		t.Error("sign-in checks turned off while open")
	}
	if err := s.Command("/Allowlist  OFF"); err == nil {
		t.Error("allowlist turned off from the console while open")
	}
	text, _ := s.RawProperties()
	if err := s.SaveRawProperties(strings.Replace(text, "allow-list=true", "allow-list=false", 1)); err == nil {
		t.Error("raw editor turned the allowlist off while open")
	}
	// Closed, it's allowed; and then it can't be opened again.
	if err := s.SetOutside(false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAllowlistEnabled(false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOutside(true); err == nil || !strings.Contains(err.Error(), "allowlist is off") {
		t.Errorf("opened with the allowlist off: %v", err)
	}
	// It's remembered across restarts.
	s.SetAllowlistEnabled(true)
	if err := s.SetOutside(true); err != nil {
		t.Fatal(err)
	}
	m.StopAll()
	m2 := New(Options{DataDir: m.opts.DataDir})
	if err := m2.Load(); err != nil {
		t.Fatal(err)
	}
	s2, _ := m2.Get(s.ID())
	if !s2.Outside() {
		t.Error("not remembered")
	}
}
