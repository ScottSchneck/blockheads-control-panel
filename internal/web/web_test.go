package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ScottSchneck/blockheads-control-panel/internal/auth"
	"github.com/ScottSchneck/blockheads-control-panel/internal/servers"
)

const testPassword = "correct horse battery"

// testCookies holds each test server's signed-in session.
var testCookies sync.Map

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	s, dir := newBareServer(t)
	code := strings.TrimSpace(readFile(t, filepath.Join(dir, "setup-code")))
	token, err := s.auth.Setup(context.Background(), "192.0.2.1", code, "Scott", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	testCookies.Store(s, &http.Cookie{Name: s.cookieName(), Value: token})
	return s, dir
}

// newBareServer has no owner yet.
func newBareServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	mgr := servers.New(servers.Options{DataDir: dir, DownloadAPI: "http://127.0.0.1:1/none"})
	if err := mgr.Load(); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{DataDir: dir, TLS: true}, mgr)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func signIn(r *http.Request, s *Server) {
	c, _ := testCookies.Load(s)
	r.AddCookie(c.(*http.Cookie))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSignInAndCrossSiteProtection(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.routes()
	do := func(method, path string, signedIn, header bool) int {
		r := httptest.NewRequest(method, path, strings.NewReader(`{"name":"x"}`))
		if signedIn {
			signIn(r, s)
		}
		if header {
			r.Header.Set("X-Blockheads", "1")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if c := do("GET", "/api/servers", false, false); c != http.StatusUnauthorized {
		t.Errorf("not signed in: %d", c)
	}
	if c := do("GET", "/", false, false); c != http.StatusOK {
		t.Errorf("the page itself is public: %d", c)
	}
	if c := do("GET", "/api/auth/state", false, false); c != http.StatusOK {
		t.Errorf("state is public: %d", c)
	}
	if c := do("GET", "/api/servers", true, false); c != http.StatusOK {
		t.Errorf("signed in: %d", c)
	}
	if c := do("POST", "/api/servers", true, false); c != http.StatusForbidden {
		t.Errorf("POST without header: %d", c)
	}
	if c := do("POST", "/api/auth/login", false, false); c != http.StatusForbidden {
		t.Errorf("sign-in without header: %d", c)
	}
	if c := do("POST", "/api/servers", true, true); c != http.StatusBadRequest {
		t.Errorf("POST without EULA should be refused: %d", c)
	}
	if c := do("POST", "/api/servers/nope/start", true, true); c != http.StatusNotFound {
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
	signIn(r, s)
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
	signIn(r, s)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control %q", got)
	}
}

func TestImportListWithoutFolder(t *testing.T) {
	s, _ := newTestServer(t)
	r := httptest.NewRequest("GET", "/api/import", nil)
	signIn(r, s)
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
	signIn(r, s)
	r.Header.Set("X-Blockheads", "1")
	w = httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("import without a folder: %d %s", w.Code, w.Body)
	}
}

func TestBackupRoutes(t *testing.T) {
	s, dir := newTestServer(t)
	sdir := filepath.Join(dir, "servers", "empty")
	os.MkdirAll(sdir, 0o755)
	os.WriteFile(filepath.Join(sdir, ".blockheads.json"), []byte(`{"id":"empty","name":"Empty","type":"bedrock","port":19134,"portV6":19135}`), 0o644)
	os.WriteFile(filepath.Join(sdir, "bedrock_server"), []byte("x"), 0o755)
	if err := s.mgr.Load(); err != nil {
		t.Fatal(err)
	}
	do := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		signIn(r, s)
		r.Header.Set("X-Blockheads", "1")
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, r)
		return w
	}
	w := do("GET", "/api/servers/empty/backups", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "null") || !strings.Contains(w.Body.String(), `"every":"daily"`) {
		t.Errorf("list: %d %s", w.Code, w.Body)
	}
	if w := do("PUT", "/api/servers/empty/backups/plan", `{"every":"6h","keep":3}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"every":"6h"`) {
		t.Errorf("plan: %d %s", w.Code, w.Body)
	}
	if w := do("PUT", "/api/servers/empty/backups/plan", `{"every":"weekly","keep":3}`); w.Code != 400 {
		t.Errorf("bad plan: %d", w.Code)
	}
	for _, name := range []string{"..%2F..%2Fpanel-password", "empty-20260101-000000-manual.tar.gz"} {
		if w := do("GET", "/api/servers/empty/backups/"+name, ""); w.Code != 404 {
			t.Errorf("download %s: %d", name, w.Code)
		}
	}
}

// call makes a request with the X-Blockheads header and the given cookies,
// returning the response.
func call(h http.Handler, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-Blockheads", "1")
	r.RemoteAddr = "192.0.2.9:5555"
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func sessionFrom(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == "__Host-bh_session" && c.Value != "" {
			if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
				t.Errorf("cookie flags: %+v", c)
			}
			return c
		}
	}
	t.Fatalf("no session cookie: %d %s", w.Code, w.Body)
	return nil
}

func TestSetupSignInAndOut(t *testing.T) {
	s, dir := newBareServer(t)
	h := s.routes()
	var st map[string]any
	json.Unmarshal(call(h, "GET", "/api/auth/state", "").Body.Bytes(), &st)
	if st["mode"] != "setup" {
		t.Fatalf("state: %v", st)
	}
	code := strings.TrimSpace(readFile(t, filepath.Join(dir, "setup-code")))
	if w := call(h, "POST", "/api/auth/setup", `{"code":"wrong","username":"Scott","password":"long enough pw"}`); w.Code != 401 {
		t.Errorf("wrong code: %d", w.Code)
	}
	if w := call(h, "POST", "/api/auth/setup", `{"code":"`+code+`","username":"Scott","password":"short"}`); w.Code != 400 {
		t.Errorf("short password: %d", w.Code)
	}
	// The code works without dashes and in capitals too.
	w := call(h, "POST", "/api/auth/setup", `{"code":"`+strings.ToUpper(strings.ReplaceAll(code, "-", ""))+`","username":"Scott","password":"long enough pw"}`)
	cookie := sessionFrom(t, w)
	if _, err := os.Stat(filepath.Join(dir, "setup-code")); err == nil {
		t.Error("setup code should be used up")
	}
	if w := call(h, "POST", "/api/auth/setup", `{"code":"`+code+`","username":"Eve","password":"long enough pw"}`); w.Code == 200 {
		t.Error("second setup allowed")
	}
	if w := call(h, "GET", "/api/servers", "", cookie); w.Code != 200 {
		t.Errorf("signed in after setup: %d", w.Code)
	}
	json.Unmarshal(call(h, "GET", "/api/auth/state", "", cookie).Body.Bytes(), &st)
	if st["mode"] != "signedIn" || st["username"] != "Scott" {
		t.Errorf("state: %v", st)
	}
	// Sign out, then back in (the username isn't case-sensitive).
	call(h, "POST", "/api/auth/logout", "", cookie)
	if w := call(h, "GET", "/api/servers", "", cookie); w.Code != 401 {
		t.Errorf("after sign-out: %d", w.Code)
	}
	if w := call(h, "POST", "/api/auth/login", `{"username":"Scott","password":"nope nope"}`); w.Code != 401 {
		t.Errorf("wrong password: %d", w.Code)
	}
	cookie = sessionFrom(t, call(h, "POST", "/api/auth/login", `{"username":"scott","password":"long enough pw"}`))
	// Sessions survive a panel restart.
	s2, err := New(Options{DataDir: dir, TLS: true}, s.mgr)
	if err != nil {
		t.Fatal(err)
	}
	if w := call(s2.routes(), "GET", "/api/servers", "", cookie); w.Code != 200 {
		t.Errorf("after restart: %d", w.Code)
	}
}

func TestOldPanelPasswordIsTheSetupCode(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "panel-password"), []byte("oldsecretpassword\n"), 0o600)
	mgr := servers.New(servers.Options{DataDir: dir})
	mgr.Load()
	s, err := New(Options{DataDir: dir, TLS: true}, mgr)
	if err != nil {
		t.Fatal(err)
	}
	h := s.routes()
	var st map[string]any
	json.Unmarshal(call(h, "GET", "/api/auth/state", "").Body.Bytes(), &st)
	if st["usesOldPassword"] != true {
		t.Errorf("state: %v", st)
	}
	sessionFrom(t, call(h, "POST", "/api/auth/setup", `{"code":"oldsecretpassword","username":"Scott","password":"long enough pw"}`))
	if _, err := os.Stat(filepath.Join(dir, "panel-password")); err == nil {
		t.Error("old password file should be removed")
	}
}

func TestLockoutAndReset(t *testing.T) {
	s, dir := newTestServer(t)
	h := s.routes()
	for i := 0; i < 5; i++ {
		call(h, "POST", "/api/auth/login", `{"username":"Scott","password":"wrong wrong"}`)
	}
	w := call(h, "POST", "/api/auth/login", `{"username":"Scott","password":"`+testPassword+`"}`)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Errorf("should be locked: %d %s", w.Code, w.Body)
	}
	// A reset needs the code from the command line.
	if w := call(h, "POST", "/api/auth/reset", `{"code":"x","password":"a new password"}`); w.Code == 200 {
		t.Error("reset without a code")
	}
	var st map[string]any
	json.Unmarshal(call(h, "GET", "/api/auth/state", "").Body.Bytes(), &st)
	if st["resetPending"] != false {
		t.Errorf("state: %v", st)
	}
	code, err := auth.WriteResetCode(dir)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(call(h, "GET", "/api/auth/state", "").Body.Bytes(), &st)
	if st["resetPending"] != true || st["mode"] != "login" {
		t.Errorf("state: %v", st)
	}
	// Still locked from this address, so use another one.
	r := httptest.NewRequest("POST", "/api/auth/reset", strings.NewReader(`{"code":"`+code+`","password":"a new password"}`))
	r.Header.Set("X-Blockheads", "1")
	r.RemoteAddr = "192.0.2.50:1"
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, r)
	fresh := sessionFrom(t, rw)
	old, _ := testCookies.Load(s)
	if w := call(h, "GET", "/api/servers", "", old.(*http.Cookie)); w.Code != 401 {
		t.Error("a reset should sign every browser out")
	}
	if w := call(h, "GET", "/api/servers", "", fresh); w.Code != 200 {
		t.Error("the resetting browser should be signed in")
	}
	// The code works once.
	if w := call(h, "POST", "/api/auth/reset", `{"code":"`+code+`","password":"another password"}`); w.Code == 200 {
		t.Error("code reused")
	}
}

func TestChangePasswordSignsOutOtherBrowsers(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.routes()
	c1, _ := testCookies.Load(s)
	mine := c1.(*http.Cookie)
	other := sessionFrom(t, call(h, "POST", "/api/auth/login", `{"username":"Scott","password":"`+testPassword+`"}`))
	if w := call(h, "POST", "/api/auth/password", `{"current":"wrong","new":"a new password"}`, mine); w.Code == 200 {
		t.Error("wrong current password accepted")
	}
	if w := call(h, "POST", "/api/auth/password", `{"current":"`+testPassword+`","new":"a new password"}`, mine); w.Code != 200 {
		t.Fatalf("change: %d %s", w.Code, w.Body)
	}
	if call(h, "GET", "/api/servers", "", mine).Code != 200 || call(h, "GET", "/api/servers", "", other).Code != 401 {
		t.Error("only this browser should stay signed in")
	}
	sessionFrom(t, call(h, "POST", "/api/auth/login", `{"username":"Scott","password":"a new password"}`))
}

func TestSetupGuide(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.routes()
	c, _ := testCookies.Load(s)
	w := call(h, "GET", "/api/setup-guide", "", c.(*http.Cookie))
	if w.Code != 200 || strings.Contains(w.Body.String(), "null") || !strings.Contains(w.Body.String(), `"setupDone":false`) {
		t.Errorf("guide: %d %s", w.Code, w.Body)
	}
	if w := call(h, "POST", "/api/setup-guide/gamertag", `{"gamertag":"Kemikal Halo","everywhere":true}`, c.(*http.Cookie)); w.Code != 200 {
		t.Errorf("gamertag: %d %s", w.Code, w.Body)
	}
	call(h, "POST", "/api/setup-guide/done", `{"done":true}`, c.(*http.Cookie))
	w = call(h, "GET", "/api/setup-guide", "", c.(*http.Cookie))
	if !strings.Contains(w.Body.String(), `"setupDone":true`) || !strings.Contains(w.Body.String(), `"ownerGamertag":"Kemikal Halo"`) {
		t.Errorf("guide after: %s", w.Body)
	}
}

func TestPrefsAndUpdates(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.routes()
	c, _ := testCookies.Load(s)
	ck := c.(*http.Cookie)
	if w := call(h, "PUT", "/api/auth/prefs", `{"mode":"light","style":"control"}`, ck); w.Code != 200 || !strings.Contains(w.Body.String(), `"mode":"light"`) {
		t.Errorf("prefs: %d %s", w.Code, w.Body)
	}
	if w := call(h, "PUT", "/api/auth/prefs", `{"mode":"purple"}`, ck); w.Code != 400 {
		t.Errorf("bad pref: %d", w.Code)
	}
	var st map[string]any
	json.Unmarshal(call(h, "GET", "/api/auth/state", "", ck).Body.Bytes(), &st)
	if p, _ := st["prefs"].(map[string]any); p["mode"] != "light" {
		t.Errorf("state prefs: %v", st)
	}
	if w := call(h, "GET", "/api/updates", "", ck); w.Code != 200 || !strings.Contains(w.Body.String(), `"latest"`) {
		t.Errorf("updates: %d %s", w.Code, w.Body)
	}
	for _, f := range []string{"/theme.js", "/fonts/ibm-plex-sans-latin-400-normal.woff2"} {
		if w := call(h, "GET", f, ""); w.Code != 200 {
			t.Errorf("%s: %d", f, w.Code)
		}
	}
}
