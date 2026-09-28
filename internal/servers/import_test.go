package servers

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCrafty makes a folder laid out like Crafty's servers folder.
func fakeCrafty(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	make1 := func(id, name, port, world string) {
		dir := filepath.Join(root, id)
		os.MkdirAll(filepath.Join(dir, "worlds", world, "db"), 0o755)
		os.WriteFile(filepath.Join(dir, "bedrock_server"), []byte(strings.ReplaceAll(fakeServer, "VERSION", "1.26.52.3")), 0o644) // execute bit lost, as with Crafty
		props := "server-name=" + name + "\nserver-port=" + port + "\nserver-portv6=" + port + "\nlevel-name=" + world + "\nallow-list=true\nenable-lan-visibility=true\n"
		os.WriteFile(filepath.Join(dir, "server.properties"), []byte(props), 0o644)
		os.WriteFile(filepath.Join(dir, "allowlist.json"), []byte(`[{"ignoresPlayerLimit":false,"name":"Kemikal Halo","xuid":"2533274815313881"}]`), 0o644)
		os.WriteFile(filepath.Join(dir, "permissions.json"), []byte(`[{"permission":"operator","xuid":"2533274815313881"}]`), 0o644)
		os.WriteFile(filepath.Join(dir, "worlds", world, "level.dat"), []byte(strings.Repeat("w", 5000)), 0o644)
		os.WriteFile(filepath.Join(dir, "worlds", world, "db", "000001.ldb"), []byte("chunks"), 0o644)
	}
	make1("00befa15-a41f-4a6f-a748-4c1aaaa2f1dd", "Blockheads Minecraft Server", "19144", "Blockheads World")
	make1("8835f391-da7c-4090-bd1b-23a5e98dc98f", "Ava's Server", "19140", "Ava World")
	os.MkdirAll(filepath.Join(root, "not-a-server"), 0o755) // ignored
	return root
}

func newImportManager(t *testing.T, importDir string) *Manager {
	t.Helper()
	m := New(Options{DataDir: t.TempDir(), ImportDir: importDir, StopTimeout: 2 * time.Second})
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.StopAll)
	return m
}

func TestScanImports(t *testing.T) {
	m := newImportManager(t, fakeCrafty(t))
	list, err := m.ScanImports()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("found %d: %+v", len(list), list)
	}
	a, b := list[0], list[1] // sorted by name
	if a.Name != "Ava's Server" || a.Port != 19140 || a.World != "Ava World" || !a.PortAvailable || a.Imported {
		t.Errorf("first: %+v", a)
	}
	if b.Name != "Blockheads Minecraft Server" || b.Port != 19144 || b.Worlds < 5000 {
		t.Errorf("second: %+v", b)
	}
	if !strings.HasPrefix(b.Path, "00befa15") {
		t.Errorf("path should be relative: %s", b.Path)
	}

	none := New(Options{DataDir: t.TempDir(), ImportDir: filepath.Join(t.TempDir(), "missing")})
	if _, err := none.ScanImports(); !errors.Is(err, ErrNoImportDir) {
		t.Errorf("missing folder: %v", err)
	}
}

func TestImportKeepsPortAndLeavesOriginal(t *testing.T) {
	root := fakeCrafty(t)
	before := dirSize(root)
	m := newImportManager(t, root)
	list, _ := m.ScanImports()
	bh := list[1]
	st, err := m.Import(bh.Path, bh.Name, bh.Port, true)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateImporting || st.Port != 19144 {
		t.Errorf("status %+v", st)
	}
	s, _ := m.Get(st.ID)
	st = waitFor(t, s, "running after import", state(StateRunning))
	if st.Port != 19144 || st.Version != "1.26.52.3" {
		t.Errorf("after import: %+v", st)
	}
	// World, allowlist and operators came across; the panel's port settings are applied.
	if readFile(t, filepath.Join(s.dir(), "worlds", "Blockheads World", "db", "000001.ldb")) != "chunks" {
		t.Error("world not copied")
	}
	if !strings.Contains(readFile(t, filepath.Join(s.dir(), "allowlist.json")), "Kemikal Halo") {
		t.Error("allowlist not copied")
	}
	props := readFile(t, filepath.Join(s.dir(), "server.properties"))
	for _, want := range []string{"server-port=19144", "server-portv6=19145", "enable-lan-visibility=false", "level-name=Blockheads World"} {
		if !strings.Contains(props, want) {
			t.Errorf("server.properties missing %q", want)
		}
	}
	v := mustPlayers(t, s)
	if len(v.Operators) != 1 || len(v.Allowlist) != 1 {
		t.Errorf("players: %+v", v)
	}
	// The original is untouched.
	if dirSize(root) != before {
		t.Error("the original folder changed")
	}
	if strings.Contains(readFile(t, filepath.Join(root, bh.Path, "server.properties")), "enable-lan-visibility=false") {
		t.Error("the original server.properties was edited")
	}
	// It shows as imported, and can't be imported twice.
	list, _ = m.ScanImports()
	if !list[1].Imported || list[1].ImportedAs != bh.Name || list[1].PortAvailable {
		t.Errorf("scan after import: %+v", list[1])
	}
	if _, err := m.Import(bh.Path, "Another name", 0, false); err == nil {
		t.Error("importing the same folder twice should be refused")
	}
	if got := m.ImportResults(); len(got) != 1 || !got[0].OK {
		t.Errorf("results: %+v", got)
	}
	// It survives a panel restart, still marked as imported.
	m.StopAll()
	m2 := New(Options{DataDir: m.opts.DataDir, ImportDir: root})
	m2.Load()
	if s2, ok := m2.Get(st.ID); !ok || s2.Status().Port != 19144 {
		t.Error("not loaded after restart")
	}
	if l2, _ := m2.ScanImports(); !l2[1].Imported {
		t.Error("imported mark lost after restart")
	}
}

func TestImportRefusals(t *testing.T) {
	root := fakeCrafty(t)
	m := newImportManager(t, root)
	list, _ := m.ScanImports()
	ava := list[0]
	cases := []struct {
		path, name string
		port       int
	}{
		{"../escape", "X", 0},
		{"/etc", "X", 0},
		{"not-a-server", "X", 0},
		{ava.Path, "", 0},
		{ava.Path, "X", 19132}, // the server list's port
		{ava.Path, "X", 19199}, // needs 19200 for IPv6
		{ava.Path, "X", 30000},
	}
	for _, c := range cases {
		if _, err := m.Import(c.path, c.name, c.port, false); err == nil {
			t.Errorf("%+v should be refused", c)
		}
	}
	// A port already taken by another server (or its IPv6 port).
	if _, err := m.Import(ava.Path, "Ava's Server", 19140, false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Import(list[1].Path, "Blockheads", 19141, false); err == nil {
		t.Error("a port overlapping another server's should be refused")
	}
	if _, err := m.Import(list[1].Path, "ava's server", 19144, false); err == nil {
		t.Error("a duplicate name should be refused")
	}
	// Port 0 picks a free one.
	st, err := m.Import(list[1].Path, "Blockheads", 0, false)
	if err != nil || st.Port == 19140 || st.Port == 19141 {
		t.Errorf("auto port: %+v %v", st, err)
	}
	s, _ := m.Get(st.ID)
	st = waitFor(t, s, "imported and left stopped", state(StateStopped))
	if st.AutoStart {
		t.Error("start=false should leave it stopped next time too")
	}
}

func TestFailedImportLeavesNothing(t *testing.T) {
	root := fakeCrafty(t)
	m := newImportManager(t, root)
	list, _ := m.ScanImports()
	copyFileHook = func(path string) error {
		if strings.HasSuffix(path, ".ldb") {
			return errors.New("disk full")
		}
		return nil
	}
	defer func() { copyFileHook = nil }()
	st, err := m.Import(list[1].Path, list[1].Name, 19144, true)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := m.Get(st.ID); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed import still listed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	res := m.ImportResults()
	if len(res) != 1 || res[0].OK || !strings.Contains(res[0].Error, "disk full") {
		t.Errorf("result: %+v", res)
	}
	left, _ := filepath.Glob(filepath.Join(m.opts.DataDir, "servers", "*"))
	hidden, _ := filepath.Glob(filepath.Join(m.opts.DataDir, "servers", ".*"))
	if len(left)+len(hidden) != 0 {
		t.Errorf("left behind: %v %v", left, hidden)
	}
	// It can be tried again.
	copyFileHook = nil
	st, err = m.Import(list[1].Path, list[1].Name, 19144, false)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	s, _ := m.Get(st.ID)
	waitFor(t, s, "retry imported", state(StateStopped))
}

func TestImportPathSpellingsAndOddPorts(t *testing.T) {
	root := fakeCrafty(t)
	m := newImportManager(t, root)
	list, _ := m.ScanImports()
	ava := list[0]
	if _, err := m.Import(ava.Path, "Ava", 19141, false); err == nil {
		t.Error("an odd port should be refused")
	}
	st, err := m.Import(ava.Path+"/", "Ava", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{ava.Path, "./" + ava.Path, ava.Path + "/../" + ava.Path} {
		if _, err := m.Import(p, "Ava again "+p[:1], 0, false); err == nil {
			t.Errorf("%q: the same folder imported twice", p)
		}
	}
	s, _ := m.Get(st.ID)
	waitFor(t, s, "imported", state(StateStopped))
	if l, _ := m.ScanImports(); !l[0].Imported {
		t.Error("not marked imported")
	}
}

func TestLinkedWorldIsRefused(t *testing.T) {
	root := fakeCrafty(t)
	elsewhere := t.TempDir()
	os.MkdirAll(filepath.Join(elsewhere, "Linked World", "db"), 0o755)
	dir := filepath.Join(root, "8835f391-da7c-4090-bd1b-23a5e98dc98f")
	os.RemoveAll(filepath.Join(dir, "worlds", "Ava World"))
	if err := os.Symlink(filepath.Join(elsewhere, "Linked World"), filepath.Join(dir, "worlds", "Ava World")); err != nil {
		t.Skip("no symlinks here")
	}
	m := newImportManager(t, root)
	st, err := m.Import("8835f391-da7c-4090-bd1b-23a5e98dc98f", "Ava", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := m.Get(st.ID); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("import with a linked world should fail")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if res := m.ImportResults(); len(res) != 1 || !strings.Contains(res[0].Error, "link") {
		t.Errorf("result: %+v", res)
	}
}

func TestNoChangesWhileImporting(t *testing.T) {
	root := fakeCrafty(t)
	m := newImportManager(t, root)
	list, _ := m.ScanImports()
	release := make(chan struct{})
	copyFileHook = func(string) error { <-release; return nil }
	defer func() { copyFileHook = nil }()
	st, err := m.Import(list[0].Path, "Ava", 0, false)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	s, _ := m.Get(st.ID)
	if !s.Importing() {
		t.Error("should be importing")
	}
	if err := s.Rename("Other"); !errors.Is(err, ErrImporting) {
		t.Errorf("rename: %v", err)
	}
	if err := s.Stop(); !errors.Is(err, ErrImporting) {
		t.Errorf("stop: %v", err)
	}
	if err := s.Start(); err == nil {
		t.Error("start should be refused")
	}
	if err := s.Update(); err == nil {
		t.Error("update should be refused")
	}
	close(release)
	st = waitFor(t, s, "imported", state(StateStopped))
	if st.Name != "Ava" || st.AutoStart {
		t.Errorf("after: %+v", st)
	}
}

func TestUnreadableFolderIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read everything")
	}
	root := fakeCrafty(t)
	locked := filepath.Join(root, "locked")
	os.MkdirAll(locked, 0o000)
	defer os.Chmod(locked, 0o755)
	m := newImportManager(t, root)
	list, err := m.ScanImports()
	if len(list) != 2 || err == nil || !strings.Contains(err.Error(), "locked") {
		t.Errorf("%d found, error %v", len(list), err)
	}
}
