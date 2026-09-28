package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ScottSchneck/blockheads-control-panel/internal/servers"
)

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	mgr := servers.New(servers.Options{DataDir: dir})
	if err := mgr.Load(); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{DataDir: dir}, mgr)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestPasswordIsGeneratedOnceAndKept(t *testing.T) {
	s, dir := newTestServer(t)
	if len(s.password) < 16 {
		t.Fatalf("password too short: %q", s.password)
	}
	b, err := os.ReadFile(filepath.Join(dir, "panel-password"))
	if err != nil || strings.TrimSpace(string(b)) != s.password {
		t.Fatal("password not saved")
	}
	again, err := New(Options{DataDir: dir}, nil)
	if err != nil || again.password != s.password {
		t.Fatal("password changed on restart")
	}
	chosen, _ := New(Options{DataDir: dir, Password: "mine"}, nil)
	if chosen.password != "mine" {
		t.Fatal("PANEL_PASSWORD ignored")
	}
}

func TestSignInAndCrossSiteProtection(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.routes()
	do := func(method, path, password string, header bool) int {
		r := httptest.NewRequest(method, path, strings.NewReader(`{"name":"x"}`))
		if password != "" {
			r.SetBasicAuth("anyone", password)
		}
		if header {
			r.Header.Set("X-Blockheads", "1")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if c := do("GET", "/api/servers", "", false); c != http.StatusUnauthorized {
		t.Errorf("no password: %d", c)
	}
	if c := do("GET", "/", "", false); c != http.StatusUnauthorized {
		t.Errorf("page without password: %d", c)
	}
	if c := do("GET", "/api/servers", s.password, false); c != http.StatusOK {
		t.Errorf("with password: %d", c)
	}
	if c := do("GET", "/", s.password, false); c != http.StatusOK {
		t.Errorf("page with password: %d", c)
	}
	if c := do("POST", "/api/servers", s.password, false); c != http.StatusForbidden {
		t.Errorf("POST without header: %d", c)
	}
	if c := do("POST", "/api/servers", s.password, true); c != http.StatusBadRequest {
		t.Errorf("POST without EULA should be refused: %d", c)
	}
	if c := do("POST", "/api/servers/nope/start", s.password, true); c != http.StatusNotFound {
		t.Errorf("unknown server: %d", c)
	}
}
