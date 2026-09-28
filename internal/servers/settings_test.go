package servers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newSettingsServer sets up a stopped server whose server.properties is the
// real file from a Bedrock 1.26.52 server.
func newSettingsServer(t *testing.T) (*Manager, *Server) {
	t.Helper()
	dir := t.TempDir()
	m := New(Options{DataDir: dir})
	sdir := filepath.Join(dir, "servers", "blockheads")
	os.MkdirAll(sdir, 0o755)
	os.WriteFile(filepath.Join(sdir, metaFile), []byte(`{"id":"blockheads","name":"Blockheads","type":"bedrock","port":19134,"portV6":19135}`), 0o644)
	os.WriteFile(filepath.Join(sdir, "bedrock_server"), []byte("x"), 0o755)
	real, err := os.ReadFile("testdata/bedrock-1.26.52.properties")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(sdir, "server.properties"), real, 0o644)
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get("blockheads")
	return m, s
}

func fieldValue(t *testing.T, v SettingsView, key string) Field {
	t.Helper()
	for _, g := range v.Groups {
		for _, f := range g.Fields {
			if f.Key == key {
				return f
			}
		}
	}
	t.Fatalf("no field %s", key)
	return Field{}
}

func TestSettingsReadRealFile(t *testing.T) {
	_, s := newSettingsServer(t)
	v, err := s.Settings()
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"gamemode": "survival", "difficulty": "easy", "view-distance": "12", "tick-distance": "6",
		"level-name": "Blockheads World", "level-seed": "3521935383318044165", "transport": "nethernet",
		"allow-list": "true", "chat-restriction": "None", "disable-custom-skins": "true", "server-name": "Blockheads Minecraft Server",
	} {
		if f := fieldValue(t, v, key); f.Value != want || !f.InFile {
			t.Errorf("%s = %q (inFile %v), want %q", key, f.Value, f.InFile, want)
		}
	}
	// Every select value in the real file is one of the choices.
	for _, g := range v.Groups {
		for _, f := range g.Fields {
			if f.Type != "select" {
				continue
			}
			ok := false
			for _, o := range f.Options {
				ok = ok || o.Value == f.Value
			}
			if !ok {
				t.Errorf("%s value %q isn't a choice", f.Key, f.Value)
			}
		}
	}
}

func TestSettingsSaveOnlyChangesThoseLines(t *testing.T) {
	_, s := newSettingsServer(t)
	before, _ := os.ReadFile(s.propertiesPath())
	changes, err := s.SaveSettings(map[string]string{
		"gamemode": "creative", "max-players": "12", "difficulty": "easy", // difficulty unchanged
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes: %+v", changes)
	}
	after, _ := os.ReadFile(s.propertiesPath())
	b, a := strings.Split(string(before), "\n"), strings.Split(string(after), "\n")
	if len(a) != len(b) {
		t.Fatalf("line count changed: %d → %d", len(b), len(a))
	}
	var diff []string
	for i := range b {
		if b[i] != a[i] {
			diff = append(diff, a[i])
		}
	}
	if strings.Join(diff, "|") != "gamemode=creative|max-players=12" {
		t.Errorf("changed lines: %v", diff)
	}
	if !consoleHas(s, "Setting changed: Game mode: survival → creative") {
		t.Error("change not logged to the console")
	}
}

func TestSettingsRejectBadValues(t *testing.T) {
	_, s := newSettingsServer(t)
	before, _ := os.ReadFile(s.propertiesPath())
	bad := []map[string]string{
		{"difficulty": "nightmare"},
		{"max-players": "0"},
		{"max-players": "ten"},
		{"view-distance": "4"},
		{"allow-cheats": "yes"},
		{"level-name": "../../etc"},
		{"server-name": "a;b"},
		{"server-name": "line\nbreak"},
		{"server-port": "19132"},                        // managed by the panel
		{"allow-list": "false"},                         // on the Players tab
		{"level-seed": "123"},                           // read-only
		{"compression-algorithm": "snappy"},             // not on the Settings tab
		{"gamemode": "creative", "tick-distance": "99"}, // one bad value stops the lot
	}
	for _, values := range bad {
		if _, err := s.SaveSettings(values); err == nil {
			t.Errorf("%v should be refused", values)
		}
	}
	after, _ := os.ReadFile(s.propertiesPath())
	if string(after) != string(before) {
		t.Error("a refused save changed the file")
	}
}

func TestSettingsAddsMissingKeys(t *testing.T) {
	_, s := newSettingsServer(t)
	os.WriteFile(s.propertiesPath(), []byte("server-port=19134\n"), 0o644)
	v, _ := s.Settings()
	if f := fieldValue(t, v, "gamemode"); f.InFile || f.Value != "survival" {
		t.Errorf("missing key should show Bedrock's default: %+v", f)
	}
	if _, err := s.SaveSettings(map[string]string{"gamemode": "adventure"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(s.propertiesPath())
	if !strings.Contains(string(b), "gamemode=adventure") {
		t.Errorf("not added: %s", b)
	}
}

func TestRawPropertiesKeepPanelSettings(t *testing.T) {
	_, s := newSettingsServer(t)
	if err := s.SaveRawProperties("gamemode=creative\r\nserver-port=1\nenable-lan-visibility=true\n"); err != nil {
		t.Fatal(err)
	}
	text, _ := s.RawProperties()
	for _, want := range []string{"gamemode=creative", "server-port=19134", "enable-lan-visibility=false"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\r") {
		t.Error("Windows line endings kept")
	}
	if err := s.SaveRawProperties(strings.Repeat("x", maxRawProperties+1)); err == nil {
		t.Error("huge file should be refused")
	}
	if err := s.SaveRawProperties("a=\x00"); err == nil {
		t.Error("binary content should be refused")
	}
}

func TestRename(t *testing.T) {
	m, s := newSettingsServer(t)
	if err := s.Rename("  Blockheads World  "); err != nil {
		t.Fatal(err)
	}
	if s.Status().Name != "Blockheads World" {
		t.Errorf("name %q", s.Status().Name)
	}
	m2 := New(Options{DataDir: m.opts.DataDir})
	m2.Load()
	if s2, _ := m2.Get("blockheads"); s2.Status().Name != "Blockheads World" {
		t.Error("rename not saved")
	}
	m.servers["other"] = newServer(m, Meta{ID: "other", Name: "Other"})
	if err := s.Rename("other"); err == nil {
		t.Error("duplicate names should be refused")
	}
	if err := s.Rename(""); err == nil {
		t.Error("empty name should be refused")
	}
}

func TestSettingsReviewFixes(t *testing.T) {
	_, s := newSettingsServer(t)
	// Trailing spaces are trimmed; numbers are normalised.
	if _, err := s.SaveSettings(map[string]string{"level-name": "Blockheads World ", "max-players": "+05"}); err != nil {
		t.Fatal(err)
	}
	text, _ := s.RawProperties()
	if !strings.Contains(text, "\nlevel-name=Blockheads World\n") || !strings.Contains(text, "\nmax-players=5\n") {
		t.Errorf("not normalised:\n%s", text)
	}
	for _, bad := range []string{"..", ".", ".hidden", "World.", "a\fb", "a\u0085b"} {
		if _, err := s.SaveSettings(map[string]string{"level-name": bad}); err == nil {
			t.Errorf("world folder %q should be refused", bad)
		}
	}
	// Processor threads: missing means Bedrock uses them all, shown as 0.
	os.WriteFile(s.propertiesPath(), []byte("server-port=19134\n"), 0o644)
	v, _ := s.Settings()
	if f := fieldValue(t, v, "max-threads"); f.Value != "0" {
		t.Errorf("max-threads default %q", f.Value)
	}
	if f := fieldValue(t, v, "transport"); f.Value != "" {
		t.Errorf("transport shouldn't guess a default: %q", f.Value)
	}
}

func TestRawEditorRefusesDuplicatesAndBareCR(t *testing.T) {
	_, s := newSettingsServer(t)
	err := s.SaveRawProperties("enable-lan-visibility=false\nenable-lan-visibility=true\nallow-list=true\nallow-list=false\n")
	if err == nil || !strings.Contains(err.Error(), "enable-lan-visibility") || !strings.Contains(err.Error(), "allow-list") {
		t.Errorf("duplicates should be refused and named: %v", err)
	}
	if err := s.SaveRawProperties("difficulty=easy\renable-lan-visibility=true\n"); err != nil {
		t.Fatal(err)
	}
	text, _ := s.RawProperties()
	if strings.Contains(text, "\r") || !strings.Contains(text, "enable-lan-visibility=false") {
		t.Errorf("bare CR not handled:\n%q", text)
	}
}

func TestPropertiesSetRemovesDuplicates(t *testing.T) {
	p := &properties{lines: []string{"a=1", "# a=comment", "b=2", "a=3"}}
	p.set("a", "9")
	if got := strings.Join(p.lines, "|"); got != "a=9|# a=comment|b=2" {
		t.Errorf("got %s", got)
	}
	if d := p.duplicates(); len(d) != 0 {
		t.Errorf("duplicates left: %v", d)
	}
}

func TestRenameWhileListing(t *testing.T) {
	m, s := newSettingsServer(t)
	m.servers["other"] = newServer(m, Meta{ID: "other", Name: "Other"})
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			m.List()
		}
		close(done)
	}()
	for i := 0; i < 50; i++ {
		s.Rename(fmt.Sprintf("Name %d", i))
	}
	<-done
}
