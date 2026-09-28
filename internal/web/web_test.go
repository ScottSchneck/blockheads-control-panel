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

func TestPlayersJSONHasNoNullLists(t *testing.T) {
	s, dir := newTestServer(t)
	// A server folder with the files Bedrock creates, but nobody in them.
	sdir := filepath.Join(dir, "servers", "empty")
	os.MkdirAll(sdir, 0o755)
	os.WriteFile(filepath.Join(sdir, ".blockheads.json"), []byte(`{"id":"empty","name":"Empty","type":"bedrock","port":19134,"portV6":19135}`), 0o644)
	os.WriteFile(filepath.Join(sdir, "bedrock_server"), []byte("x"), 0o755)
	os.WriteFile(filepath.Join(sdir, "server.properties"), []byte("allow-list=true\n"), 0o644)
	os.WriteFile(filepath.Join(sdir, "allowlist.json"), []byte("[]"), 0o644)
	os.WriteFile(filepath.Join(sdir, "permissions.json"), []byte("[]"), 0o644)
	if err := s.mgr.Load(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/servers/empty/players", nil)
	r.SetBasicAuth("x", s.password)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "null") {
		t.Errorf("lists must be [] not null: %s", w.Body)
	}
	if !strings.Contains(w.Body.String(), `"allowlistEnabled":true`) {
		t.Errorf("allowlist state: %s", w.Body)
	}
}

func TestPageIsNotCached(t *testing.T) {
	s, _ := newTestServer(t)
	r := httptest.NewRequest("GET", "/app.js", nil)
	r.SetBasicAuth("x", s.password)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control %q", got)
	}
}

func TestImportListWithoutFolder(t *testing.T) {
	s, _ := newTestServer(t)
	r := httptest.NewRequest("GET", "/api/import", nil)
	r.SetBasicAuth("x", s.password)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"available":false`) || strings.Contains(body, "null") {
		t.Errorf("unexpected: %s", body)
	}
	r = httptest.NewRequest("POST", "/api/import", strings.NewReader(`{"path":"a","name":"A"}`))
	r.SetBasicAuth("x", s.password)
	r.Header.Set("X-Blockheads", "1")
	w = httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("import without a folder: %d %s", w.Code, w.Body)
	}
}
