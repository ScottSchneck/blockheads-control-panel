package servers

import (
	"errors"
	"fmt"
	"strings"
)

// A server can be opened to friends outside the house (see the outside
// package). That's only allowed while its allowlist and Xbox sign-in checks
// are on, so nobody on the internet can wander in or pretend to be someone
// on the list; the settings that would turn those off are refused while it's
// open.

// outsideProblem says why a server with these properties mustn't be open to
// the internet, or "".
func outsideProblem(p *properties) string {
	allow, ok := p.get("allow-list")
	if !ok {
		allow, _ = p.get("white-list")
	}
	if !strings.EqualFold(strings.TrimSpace(allow), "true") {
		return "the allowlist is off"
	}
	if v, ok := p.get("online-mode"); ok && !strings.EqualFold(strings.TrimSpace(v), "true") {
		return "Xbox sign-in checks are off"
	}
	return ""
}

// OutsideProblem says why the server can't be open to friends outside right
// now, or "".
func (s *Server) OutsideProblem() string {
	s.filesMu.Lock()
	p, err := readProperties(s.propertiesPath())
	s.filesMu.Unlock()
	if err != nil {
		return "its server.properties can't be read"
	}
	return outsideProblem(p)
}

// outsideCommand refuses console commands that would let strangers in while
// the server is open to friends outside.
func (s *Server) outsideCommand(line string) error {
	f := strings.Fields(strings.ToLower(strings.TrimPrefix(line, "/")))
	if len(f) >= 2 && (f[0] == "allowlist" || f[0] == "whitelist") && f[1] == "off" && s.Outside() {
		return ErrOutsideOpen
	}
	return nil
}

// Outside reports whether the server is marked open to friends outside.
func (s *Server) Outside() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta.Outside
}

// ErrOutsideOpen refuses a change that would let strangers in while the
// server is open to friends outside.
var ErrOutsideOpen = errors.New("this server is open to friends outside the house; close it to them first (Overview tab)")

// SetOutside opens the server to friends outside the house, or closes it.
func (s *Server) SetOutside(open bool) error {
	if open {
		if err := s.EditBlocked(); err != nil {
			return err
		}
	}
	// Check and mark it under filesMu, so nothing can turn the allowlist
	// off in between (those changes check the mark under the same locks).
	s.filesMu.Lock()
	if open {
		p, err := readProperties(s.propertiesPath())
		problem := "its server.properties can't be read"
		if err == nil {
			problem = outsideProblem(p)
		}
		if problem != "" {
			s.filesMu.Unlock()
			return fmt.Errorf("it can't be opened to friends outside while %s", problem)
		}
	}
	s.mu.Lock()
	same := s.meta.Outside == open
	s.meta.Outside = open
	s.mu.Unlock()
	s.filesMu.Unlock()
	if same {
		return nil
	}
	if open {
		s.say("Opened to friends outside the house (only players on the allowlist can join).")
	} else {
		s.say("Closed to friends outside the house.")
	}
	return s.saveMeta()
}
