package servers

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer behaves like bedrock_server as far as the panel can tell.
const fakeServer = `#!/bin/sh
echo "[2026-09-27 12:00:00:000 INFO] Starting Server"
echo "[2026-09-27 12:00:00:000 INFO] Version: VERSION"
echo "[2026-09-27 12:00:00:001 INFO] Server started."
while read line; do
  case "$line" in
    stop) echo "[INFO] Server stop requested."; echo "Quit correctly"; exit 0;;
    crash) exit 3;;
    quit) exit 0;;
    hang) trap '' TERM; while true; do sleep 1; done;;
    join\ *) echo "[2026-09-27 12:00:01:000 INFO] Player connected: ${line#join }, xuid: 2535400000000000";;
    leave\ *) echo "[2026-09-27 12:00:02:000 INFO] Player disconnected: ${line#leave }, xuid: 2535400000000000, pfid: x";;
    save\ hold) held=1; echo "Saving...";;
    save\ query) if [ -n "$held" ]; then
        echo "[2026-09-27 12:00:03:000 INFO] Data saved. Files are now ready to be copied."
        (cd worlds && find . -type f | sed 's|^\./||' | while read f; do printf '%s:%s, ' "$f" "$(wc -c < "$f" | tr -d ' ')"; done) | sed 's/, $//'; echo
      else echo "A previous save has not been completed."; fi;;
    save\ resume) held=; echo "Changes to the world are resumed.";;
    grow\ *) printf 'more' >> "worlds/${line#grow }";;
    *) echo "[INFO] got: $line";;
  esac
done
`

const defaultProperties = `server-name=Dedicated Server
# comment kept
gamemode=survival
server-port=19132
server-portv6=19133
enable-lan-visibility=true
server-udp-ports=49152-49200
allow-list=true
`

func makeZip(t *testing.T, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string, mode os.FileMode) {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	add("bedrock_server", strings.ReplaceAll(fakeServer, "VERSION", version), 0o644) // execute bit missing on purpose
	add("server.properties", defaultProperties, 0o644)
	add("allowlist.json", "[]", 0o644)
	add("permissions.json", "[]", 0o644)
	add("bedrock_server_how_to.html", "v"+version, 0o644)
	add("config/default/permissions.json", "{}", 0o644)
	zw.Close()
	return buf.Bytes()
}

// fakeMojang serves the download list and whichever version is current.
type fakeMojang struct {
	mu      sync.Mutex
	version string
	zips    map[string][]byte
	srv     *httptest.Server
}

func newFakeMojang(t *testing.T, version string) *fakeMojang {
	f := &fakeMojang{version: version, zips: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/links" {
			fmt.Fprintf(w, `{"result":{"links":[{"downloadType":"serverBedrockLinux","downloadUrl":"%s/bin-linux/bedrock-server-%s.zip"}]}}`, f.srv.URL, f.version)
			return
		}
		for v, z := range f.zips {
			if strings.HasSuffix(r.URL.Path, "bedrock-server-"+v+".zip") {
				w.Write(z)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.srv.Close)
	f.release(t, version)
	return f
}

func (f *fakeMojang) release(t *testing.T, version string) {
	z := makeZip(t, version)
	f.mu.Lock()
	f.version = version
	f.zips[version] = z
	f.mu.Unlock()
}

func waitFor(t *testing.T, s *Server, what string, ok func(Status) bool) Status {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st := s.Status()
		if ok(st) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	backlog, _, stop := s.Subscribe()
	stop()
	t.Fatalf("timed out waiting for %s; status %+v; console:\n%s", what, s.Status(), strings.Join(backlog, "\n"))
	return Status{}
}

func state(want State) func(Status) bool { return func(s Status) bool { return s.State == want } }

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestServerLifecycle(t *testing.T) {
	crashBackoff = 50 * time.Millisecond
	mojang := newFakeMojang(t, "1.26.52.3")
	data := t.TempDir()
	opts := Options{DataDir: data, DownloadAPI: mojang.srv.URL + "/links", StopTimeout: 2 * time.Second, Version: "test"}
	m := New(opts)
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}

	// Create installs and starts.
	st, err := m.Create("Test World", false)
	if err != nil {
		t.Fatal(err)
	}
	if st.ID != "test-world" || st.Port != 19134 {
		t.Fatalf("got %+v", st)
	}
	s, _ := m.Get("test-world")
	st = waitFor(t, s, "running", state(StateRunning))
	if st.Version != "1.26.52.3" {
		t.Errorf("version %q", st.Version)
	}
	props := readFile(t, filepath.Join(s.dir(), "server.properties"))
	for _, want := range []string{"server-name=Test World", "server-port=19134", "server-portv6=19135", "enable-lan-visibility=false", "server-udp-ports=49152-49201", "# comment kept"} {
		if !strings.Contains(props, want) {
			t.Errorf("server.properties is missing %q:\n%s", want, props)
		}
	}
	if got := m.Joinable(); len(got) != 1 || got[0].Port != 19134 {
		t.Errorf("joinable: %+v", got)
	}

	// A second server gets the next ports.
	st2, err := m.Create("Second", false)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Port != 19136 {
		t.Errorf("second server port %d", st2.Port)
	}
	if _, err := m.Create("second", false); err == nil {
		t.Error("duplicate names should be refused")
	}

	// Players come and go.
	if err := s.Command("join Kemikal Halo"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, "player joined", func(st Status) bool { return len(st.Players) == 1 && st.Players[0].Name == "Kemikal Halo" })
	s.Command("leave Kemikal Halo")
	waitFor(t, s, "player left", func(st Status) bool { return len(st.Players) == 0 })

	// Owner changes that an update must keep.
	propsPath := filepath.Join(s.dir(), "server.properties")
	os.WriteFile(propsPath, []byte(strings.Replace(readFile(t, propsPath), "gamemode=survival", "gamemode=creative", 1)), 0o644)
	os.WriteFile(filepath.Join(s.dir(), "allowlist.json"), []byte(`[{"name":"Kemikal Halo"}]`), 0o644)
	os.MkdirAll(filepath.Join(s.dir(), "worlds", "Bedrock level"), 0o755)
	os.WriteFile(filepath.Join(s.dir(), "worlds", "Bedrock level", "level.dat"), []byte("world"), 0o644)

	// Update to a new version while running.
	mojang.release(t, "1.26.60.1")
	if err := s.Update(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, "running the new version", func(st Status) bool { return st.State == StateRunning && st.Version == "1.26.60.1" })
	if got := readFile(t, filepath.Join(s.dir(), "bedrock_server_how_to.html")); got != "v1.26.60.1" {
		t.Errorf("program files weren't replaced: %q", got)
	}
	if !strings.Contains(readFile(t, propsPath), "gamemode=creative") {
		t.Error("update overwrote server.properties")
	}
	if !strings.Contains(readFile(t, filepath.Join(s.dir(), "allowlist.json")), "Kemikal Halo") {
		t.Error("update overwrote the allowlist")
	}
	if readFile(t, filepath.Join(s.dir(), "worlds", "Bedrock level", "level.dat")) != "world" {
		t.Error("update lost the world")
	}
	backups, _ := filepath.Glob(filepath.Join(data, "backups", "test-world", "*-before-update.tar.gz"))
	if len(backups) != 1 {
		t.Errorf("expected one backup, got %v", backups)
	}

	// Updating again changes nothing.
	s.Update()
	waitFor(t, s, "running after a no-op update", func(st Status) bool { return st.State == StateRunning && !s.isBusy() })

	// A crash restarts the server.
	s.Command("crash")
	waitFor(t, s, "restart after crash", func(st Status) bool { return st.State == StateStarting })
	waitFor(t, s, "running again after crash", state(StateRunning))

	// Stop stops it and remembers not to start it next time.
	s.Stop()
	st = waitFor(t, s, "stopped", state(StateStopped))
	if st.AutoStart {
		t.Error("autoStart should be off after Stop")
	}
	if err := s.Command("say hi"); err == nil {
		t.Error("commands to a stopped server should fail")
	}

	// A server that ignores stop is forced.
	s2, _ := m.Get("second")
	waitFor(t, s2, "second running", state(StateRunning))
	s2.Command("hang")
	time.Sleep(100 * time.Millisecond)
	s2.Stop()
	waitFor(t, s2, "forced stop", state(StateStopped))

	// After a panel restart, only servers that were running start again.
	s2.Start()
	waitFor(t, s2, "second running again", state(StateRunning))
	m.StopAll()
	waitFor(t, s2, "stopped by StopAll", state(StateStopped))

	m2 := New(opts)
	if err := m2.Load(); err != nil {
		t.Fatal(err)
	}
	m2.StartAutoStart()
	a, _ := m2.Get("test-world")
	b, _ := m2.Get("second")
	waitFor(t, b, "second autostarted", state(StateRunning))
	time.Sleep(200 * time.Millisecond)
	if a.Status().State != StateStopped {
		t.Errorf("test-world should stay stopped, is %s", a.Status().State)
	}
	if a.Status().Version != "1.26.60.1" {
		t.Errorf("version not remembered: %q", a.Status().Version)
	}
	m2.StopAll()
}

func TestInstallFailureIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()
	m := New(Options{DataDir: t.TempDir(), DownloadAPI: srv.URL})
	m.Load()
	st, err := m.Create("Broken", false)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get(st.ID)
	st = waitFor(t, s, "error", state(StateError))
	if !strings.Contains(st.Message, "403") {
		t.Errorf("message should say why: %q", st.Message)
	}
	if err := s.Start(); err == nil {
		t.Error("starting without files should fail")
	}
}

func TestExtractZipRefusesEscapes(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("../evil.txt")
	w.Write([]byte("x"))
	zw.Close()
	dir := t.TempDir()
	zp := filepath.Join(dir, "a.zip")
	os.WriteFile(zp, buf.Bytes(), 0o644)
	if err := extractZip(context.Background(), zp, filepath.Join(dir, "out")); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.txt")); err == nil {
		t.Fatal("file escaped the folder")
	}
}

func TestProperties(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.properties")
	os.WriteFile(path, []byte("# top\nlevel-name=Bedrock level\n\nmax-players = 10\n"), 0o644)
	p, err := readProperties(path)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := p.get("max-players"); v != "10" {
		t.Errorf("max-players %q", v)
	}
	p.set("max-players", "5")
	p.set("new-key", "x")
	if p.setIfPresent("missing", "y") {
		t.Error("setIfPresent added a key")
	}
	p.write(path)
	want := "# top\nlevel-name=Bedrock level\n\nmax-players=5\nnew-key=x\n"
	if got := readFile(t, path); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestIDsAndPorts(t *testing.T) {
	m := New(Options{DataDir: t.TempDir()})
	m.servers["ava-s-server"] = newServer(m, Meta{ID: "ava-s-server", Port: 19134, PortV6: 19135})
	if id := m.newID("Ava's Server"); id != "ava-s-server-2" {
		t.Errorf("id %q", id)
	}
	if id := m.newID("!!!"); id != "server" {
		t.Errorf("id %q", id)
	}
	if p, _ := m.freePort(); p != 19136 {
		t.Errorf("port %d", p)
	}
	for p := 19136; p <= lastPort; p += 2 {
		m.servers[fmt.Sprint(p)] = newServer(m, Meta{Port: p, PortV6: p + 1})
	}
	if _, err := m.freePort(); err == nil {
		t.Error("ports past 19199 must not be handed out")
	}
	if r := udpRange(19136); r != "49202-49251" {
		t.Errorf("range %s", r)
	}
}

func newTestManager(t *testing.T, mojang *fakeMojang, stopTimeout time.Duration) *Manager {
	t.Helper()
	m := New(Options{DataDir: t.TempDir(), DownloadAPI: mojang.srv.URL + "/links", StopTimeout: stopTimeout})
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.StopAll)
	return m
}

func createRunning(t *testing.T, m *Manager, name string) *Server {
	t.Helper()
	st, err := m.Create(name, false)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get(st.ID)
	waitFor(t, s, "running", state(StateRunning))
	return s
}

// A server that stops reading its input must not freeze the panel.
func TestStuckServerDoesNotFreezePanel(t *testing.T) {
	m := newTestManager(t, newFakeMojang(t, "1.0.0.1"), 1*time.Second)
	s := createRunning(t, m, "Stuck")
	s.Command("hang")
	big := strings.Repeat("x", 4000)
	var refused bool
	for i := 0; i < 200; i++ {
		if err := s.Command("say " + big); err != nil {
			refused = true
			break
		}
	}
	if !refused {
		t.Error("commands to a stuck server should eventually be refused")
	}
	done := make(chan struct{})
	go func() { m.List(); s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the panel froze")
	}
	waitFor(t, s, "forced stop", state(StateStopped))
}

// Typing stop in the console is a stop, not a crash.
func TestTypedStopIsAStop(t *testing.T) {
	crashBackoff = 50 * time.Millisecond
	m := newTestManager(t, newFakeMojang(t, "1.0.0.1"), 2*time.Second)
	s := createRunning(t, m, "Typed")
	if err := s.Command("/stop"); err != nil {
		t.Fatal(err)
	}
	st := waitFor(t, s, "stopped", state(StateStopped))
	if st.AutoStart {
		t.Error("typing stop should keep the server stopped after a restart")
	}
	time.Sleep(300 * time.Millisecond)
	if s.Status().State != StateStopped {
		t.Errorf("server came back: %s", s.Status().State)
	}

	// A clean exit we didn't ask for isn't a crash either.
	s.Start()
	waitFor(t, s, "running", state(StateRunning))
	s.Command("quit")
	waitFor(t, s, "stopped after quit", state(StateStopped))
	time.Sleep(300 * time.Millisecond)
	if s.Status().State != StateStopped {
		t.Errorf("server restarted after a clean exit: %s", s.Status().State)
	}
}

// An update that fails while copying leaves the old version in place and
// running.
func TestFailedUpdateChangesNothing(t *testing.T) {
	mojang := newFakeMojang(t, "1.0.0.1")
	m := newTestManager(t, mojang, 2*time.Second)
	s := createRunning(t, m, "Careful")
	// Make one of the new files impossible to write.
	if err := os.Mkdir(filepath.Join(s.dir(), "bedrock_server_how_to.html.new"), 0o755); err != nil {
		t.Fatal(err)
	}
	mojang.release(t, "2.0.0.1")
	s.Update()
	st := waitFor(t, s, "running again", func(st Status) bool { return st.State == StateRunning && !s.isBusy() })
	if st.Version != "1.0.0.1" {
		t.Errorf("version changed to %s", st.Version)
	}
	if !strings.Contains(st.Message, "Update failed") {
		t.Errorf("message: %q", st.Message)
	}
	if got := readFile(t, filepath.Join(s.dir(), "bedrock_server_how_to.html")); got != "v1.0.0.1" {
		t.Errorf("files changed: %q", got)
	}
	if strings.Contains(readFile(t, s.binary()), "2.0.0.1") {
		t.Error("the program was replaced")
	}
	leftovers, _ := filepath.Glob(filepath.Join(s.dir(), "*.new"))
	if len(leftovers) != 0 {
		t.Errorf("leftover files: %v", leftovers)
	}

	// The next successful update clears the notice.
	os.RemoveAll(filepath.Join(s.dir(), "bedrock_server_how_to.html.new"))
	s.Update()
	st = waitFor(t, s, "updated", func(st Status) bool { return st.State == StateRunning && st.Version == "2.0.0.1" })
	if st.Message != "" {
		t.Errorf("notice not cleared: %q", st.Message)
	}
}

// Pressing Stop during an update keeps the server stopped afterwards.
func TestStopDuringUpdateSticks(t *testing.T) {
	mojang := newFakeMojang(t, "1.0.0.1")
	m := newTestManager(t, mojang, 2*time.Second)
	s := createRunning(t, m, "Sticky")
	mojang.release(t, "2.0.0.1")
	s.Update()
	s.Stop()
	waitFor(t, s, "update finished", func(st Status) bool { return !s.isBusy() && st.Version == "2.0.0.1" })
	time.Sleep(200 * time.Millisecond)
	if st := s.Status(); st.State != StateStopped {
		t.Errorf("server started again after Stop: %s", st.State)
	}
}

// Shutting down waits for work in progress and starts nothing new.
func TestStopAllDuringInstall(t *testing.T) {
	m := newTestManager(t, newFakeMojang(t, "1.0.0.1"), 2*time.Second)
	st, _ := m.Create("Late", false)
	m.StopAll()
	s, _ := m.Get(st.ID)
	if got := s.Status().State; got == StateRunning || got == StateStarting {
		t.Errorf("server started during shutdown: %s", got)
	}
	if err := s.Start(); err == nil {
		t.Error("Start should be refused while shutting down")
	}
	if _, err := m.Create("Later", false); err == nil {
		t.Error("Create should be refused while shutting down")
	}
}

func TestLoadCleansLeftovers(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "servers", ".staging-x-123"), 0o755)
	os.WriteFile(filepath.Join(dir, "servers", ".download-1.zip"), []byte("x"), 0o644)
	m := New(Options{DataDir: dir})
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "servers", ".*"))
	if len(left) != 0 {
		t.Errorf("leftovers: %v", left)
	}
}

// A half-applied update blocks starting, now and after a panel restart,
// until a successful update clears it.
func TestHalfAppliedUpdateBlocksStart(t *testing.T) {
	mojang := newFakeMojang(t, "1.0.0.1")
	dir := t.TempDir()
	opts := Options{DataDir: dir, DownloadAPI: mojang.srv.URL + "/links", StopTimeout: 2 * time.Second}
	m := New(opts)
	m.Load()
	s := createRunning(t, m, "Half")
	s.Stop()
	waitFor(t, s, "stopped", state(StateStopped))
	os.WriteFile(s.incompleteMarker(), []byte("Update failed partway: test."), 0o644)
	if err := s.Start(); err == nil {
		t.Fatal("Start should refuse a half-applied update")
	}
	s.mu.Lock()
	s.meta.AutoStart = true
	s.mu.Unlock()
	s.saveMeta()
	m.StopAll()

	m2 := New(opts)
	m2.Load()
	m2.StartAutoStart()
	s2, _ := m2.Get(s.ID())
	if st := s2.Status(); st.State != StateError || !strings.Contains(st.Message, "partway") {
		t.Fatalf("after restart: %+v", st)
	}
	mojang.release(t, "2.0.0.1")
	s2.Update()
	waitFor(t, s2, "fixed by update", func(st Status) bool { return st.Version == "2.0.0.1" && !s2.isBusy() })
	if err := s2.Start(); err != nil {
		t.Fatalf("start after a good update: %v", err)
	}
	waitFor(t, s2, "running", state(StateRunning))
	m2.StopAll()
}
