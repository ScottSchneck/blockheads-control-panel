package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// addTestServer puts a stopped server with the given ID in the panel.
func addTestServer(t *testing.T, s *Server, dir, id, name string, port int) {
	t.Helper()
	sdir := filepath.Join(dir, "servers", id)
	os.MkdirAll(sdir, 0o755)
	meta, _ := json.Marshal(map[string]any{"id": id, "name": name, "type": "bedrock", "port": port, "portV6": port + 1})
	os.WriteFile(filepath.Join(sdir, ".blockheads.json"), meta, 0o644)
	os.WriteFile(filepath.Join(sdir, "bedrock_server"), []byte("x"), 0o755)
	os.WriteFile(filepath.Join(sdir, "server.properties"), []byte("allow-list=true\n"), 0o644)
	os.WriteFile(filepath.Join(sdir, "allowlist.json"), []byte("[]"), 0o644)
	os.WriteFile(filepath.Join(sdir, "permissions.json"), []byte("[]"), 0o644)
	if err := s.mgr.Load(); err != nil {
		t.Fatal(err)
	}
}

func ownerCookie(s *Server) *http.Cookie {
	c, _ := testCookies.Load(s)
	return c.(*http.Cookie)
}

func TestKidsSeeAndDoOnlyWhatTheyreAllowed(t *testing.T) {
	s, dir := newTestServer(t)
	addTestServer(t, s, dir, "ava", "Ava's Server", 19140)
	addTestServer(t, s, dir, "sam", "Sams Server", 19138)
	addTestServer(t, s, dir, "dad", "Schneck World", 19142)
	h := s.routes()
	owner := ownerCookie(s)

	// The owner makes accounts; nobody else can.
	if w := call(h, "POST", "/api/users", `{"username":"Ava","password":"ava password 1"}`, owner); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if w := call(h, "POST", "/api/users", `{"username":"ava","password":"another one 1"}`, owner); w.Code != 400 {
		t.Errorf("duplicate: %d", w.Code)
	}
	if w := call(h, "POST", "/api/users", `{"username":"scott","password":"another one 1"}`, owner); w.Code != 400 {
		t.Errorf("owner's name: %d", w.Code)
	}
	call(h, "POST", "/api/users", `{"username":"Sam","password":"sam password 1","canAdd":true}`, owner)
	if w := call(h, "PUT", "/api/users/Ava", `{"grants":{"ava":"care","sam":"start"}}`, owner); w.Code != 200 {
		t.Fatalf("grants: %d %s", w.Code, w.Body)
	}
	if w := call(h, "PUT", "/api/users/Ava", `{"grants":{"nope":"care"}}`, owner); w.Code != 400 {
		t.Errorf("unknown server: %d", w.Code)
	}
	if w := call(h, "PUT", "/api/users/Ava", `{"grants":{"ava":"god"}}`, owner); w.Code != 400 {
		t.Errorf("unknown role: %d", w.Code)
	}
	call(h, "PUT", "/api/users/Sam", `{"canAdd":false,"grants":{"sam":"run"}}`, owner)

	ava := sessionFrom(t, call(h, "POST", "/api/auth/login", `{"username":"ava","password":"ava password 1"}`))
	sam := sessionFrom(t, call(h, "POST", "/api/auth/login", `{"username":"Sam","password":"sam password 1"}`))

	var st map[string]any
	json.Unmarshal(call(h, "GET", "/api/auth/state", "", ava).Body.Bytes(), &st)
	if st["username"] != "Ava" || st["owner"] != false || st["canAdd"] != false || st["prefs"].(map[string]any)["style"] != "treehouse" {
		t.Errorf("state: %v", st)
	}

	// Ava sees her server and Sam's (which she can only turn on), not Dad's.
	var list []listedServer
	json.Unmarshal(call(h, "GET", "/api/servers", "", ava).Body.Bytes(), &list)
	got := map[string]int{}
	for _, x := range list {
		got[x.ID] = x.Access
	}
	if len(got) != 2 || got["ava"] != 3 || got["sam"] != 1 {
		t.Errorf("Ava's list: %v", got)
	}

	for _, c := range []struct {
		who          *http.Cookie
		method, path string
		body         string
		want         int
	}{
		{ava, "GET", "/api/servers/dad/players", "", 404},
		{ava, "POST", "/api/servers/dad/start", "", 404},
		{ava, "GET", "/api/servers/sam/players", "", 403},
		{ava, "GET", "/api/servers/sam/backups", "", 403},
		{ava, "POST", "/api/servers/sam/stop", "", 403},
		{ava, "GET", "/api/servers/ava/players", "", 200},
		{ava, "GET", "/api/servers/ava/backups", "", 200},
		{ava, "PUT", "/api/servers/ava/backups/plan", `{"every":"6h","keep":3}`, 200},
		{sam, "PUT", "/api/servers/sam/backups/plan", `{"every":"6h","keep":3}`, 403},
		{sam, "POST", "/api/servers/sam/update", "", 403},
		{sam, "GET", "/api/servers/sam/players", "", 200},
		{sam, "GET", "/api/servers/ava/players", "", 404},
		// Owner-only things.
		{ava, "GET", "/api/users", "", 403},
		{ava, "POST", "/api/users", `{"username":"Eve","password":"eve password 1"}`, 403},
		{ava, "PUT", "/api/users/Ava", `{"grants":{"dad":"care"}}`, 403},
		{ava, "GET", "/api/import", "", 403},
		{ava, "GET", "/api/setup-guide", "", 403},
		{ava, "POST", "/api/setup-guide/gamertag", `{"gamertag":"Ava"}`, 403},
		{ava, "POST", "/api/servers", `{"name":"Mine","acceptEula":true}`, 403},
	} {
		if w := call(h, c.method, c.path, c.body, c.who); w.Code != c.want {
			t.Errorf("%s %s: %d, want %d (%s)", c.method, c.path, w.Code, c.want, w.Body)
		}
	}

	// Taking Ava off Sam's server hides it from her.
	call(h, "PUT", "/api/users/Ava", `{"grants":{"sam":""}}`, owner)
	if w := call(h, "POST", "/api/servers/sam/start", "", ava); w.Code != 404 {
		t.Errorf("start after removal: %d", w.Code)
	}

	// Prefs are per account.
	call(h, "PUT", "/api/auth/prefs", `{"style":"control"}`, owner)
	var prefs map[string]string
	json.Unmarshal(call(h, "GET", "/api/auth/prefs", "", ava).Body.Bytes(), &prefs)
	if prefs["style"] != "treehouse" {
		t.Errorf("Ava's prefs changed with the owner's: %v", prefs)
	}

	// The activity log: Ava sees what happened on her server only.
	var acts []map[string]any
	json.Unmarshal(call(h, "GET", "/api/activity", "", ava).Body.Bytes(), &acts)
	if len(acts) == 0 {
		t.Fatal("no activity for Ava")
	}
	for _, a := range acts {
		if a["serverId"] != "ava" {
			t.Errorf("Ava sees %v", a)
		}
	}
	json.Unmarshal(call(h, "GET", "/api/activity", "", owner).Body.Bytes(), &acts)
	text := ""
	for _, a := range acts {
		text += a["user"].(string) + " " + a["text"].(string) + "\n"
	}
	for _, want := range []string{"Scott made an account for Ava", "Scott let Ava take care of it (Ava's Server)", "Ava changed the backup schedule", "Scott took Ava off Sams Server", "Scott stopped Sam adding servers"} {
		if !strings.Contains(text, want) {
			t.Errorf("activity lacks %q:\n%s", want, text)
		}
	}

	// A new password signs Ava out; removing her account does too.
	if w := call(h, "POST", "/api/users/Ava/password", `{"password":"new ava pass 2"}`, owner); w.Code != 200 {
		t.Fatalf("password: %d %s", w.Code, w.Body)
	}
	if w := call(h, "GET", "/api/servers", "", ava); w.Code != 401 {
		t.Errorf("still signed in after a new password: %d", w.Code)
	}
	ava = sessionFrom(t, call(h, "POST", "/api/auth/login", `{"username":"Ava","password":"new ava pass 2"}`))
	if w := call(h, "DELETE", "/api/users/ava", "", owner); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if w := call(h, "GET", "/api/servers", "", ava); w.Code != 401 {
		t.Errorf("signed in after the account was removed: %d", w.Code)
	}
	if w := call(h, "DELETE", "/api/users/ava", "", owner); w.Code != 404 {
		t.Errorf("delete twice: %d", w.Code)
	}
	if w := call(h, "DELETE", "/api/users/Scott", "", owner); w.Code != 404 {
		t.Errorf("deleting the owner: %d", w.Code)
	}
	// Signing out everywhere only signs Sam out, not the owner.
	call(h, "POST", "/api/auth/logout-all", "", sam)
	if w := call(h, "GET", "/api/servers", "", owner); w.Code != 200 {
		t.Errorf("owner signed out by Sam: %d", w.Code)
	}

	// Everything survives a restart.
	s2, err := New(Options{DataDir: dir, TLS: true}, s.mgr)
	if err != nil {
		t.Fatal(err)
	}
	var users map[string]any
	json.Unmarshal(call(s2.routes(), "GET", "/api/users", "", owner).Body.Bytes(), &users)
	if u := users["users"].([]any); len(u) != 1 || u[0].(map[string]any)["username"] != "Sam" {
		t.Errorf("users after restart: %v", users)
	}
	json.Unmarshal(call(s2.routes(), "GET", "/api/activity?limit=500", "", owner).Body.Bytes(), &acts)
	if len(acts) < 5 {
		t.Errorf("activity after restart: %d", len(acts))
	}
}

func TestAKidWhoAddsAServerTakesCareOfIt(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.routes()
	owner := ownerCookie(s)
	call(h, "POST", "/api/users", `{"username":"Sam","password":"sam password 1","canAdd":true}`, owner)
	sam := sessionFrom(t, call(h, "POST", "/api/auth/login", `{"username":"Sam","password":"sam password 1"}`))
	w := call(h, "POST", "/api/servers", `{"name":"Sam's Castle","acceptEula":true,"ownerGamertag":"SamTag"}`, sam)
	if w.Code != 202 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var st listedServer
	json.Unmarshal(w.Body.Bytes(), &st)
	var list []listedServer
	json.Unmarshal(call(h, "GET", "/api/servers", "", sam).Body.Bytes(), &list)
	if len(list) != 1 || list[0].ID != st.ID || list[0].Access != 3 {
		t.Errorf("Sam's list: %+v", list)
	}
	if s.mgr.Settings().OwnerGamertag != "" {
		t.Error("Sam's gamertag became the owner's")
	}
	if w := call(h, "POST", "/api/servers", `{"name":"Bad","acceptEula":true,"ownerGamertag":"bad\"tag"}`, sam); w.Code != 400 {
		t.Errorf("bad gamertag: %d", w.Code)
	}
	// Nobody but the owner can delete servers; there's no route at all yet.
	if w := call(h, "DELETE", "/api/servers/"+st.ID, "", sam); w.Code != 405 && w.Code != 404 {
		t.Errorf("delete: %d", w.Code)
	}
	waitFor(t, func() bool { return s.mgr.List()[0].State != "installing" })
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func TestRawEditorKeepsTheWorldInsideTheServer(t *testing.T) {
	s, dir := newTestServer(t)
	addTestServer(t, s, dir, "ava", "Ava's Server", 19140)
	w := call(s.routes(), "PUT", "/api/servers/ava/properties", `{"text":"level-name=../../dad/worlds/Bedrock level\n"}`, ownerCookie(s))
	if w.Code != 400 {
		t.Errorf("world outside the server: %d %s", w.Code, w.Body)
	}
}
