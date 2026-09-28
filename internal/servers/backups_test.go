package servers

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tarContents reads a backup into name -> contents.
func tarContents(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = string(b)
	}
}

func writeWorld(t *testing.T, s *Server, content string) {
	t.Helper()
	dir := filepath.Join(s.dir(), "worlds", "Bedrock level")
	os.MkdirAll(filepath.Join(dir, "db"), 0o755)
	os.WriteFile(filepath.Join(dir, "level.dat"), []byte("level-"+content), 0o644)
	os.WriteFile(filepath.Join(dir, "db", "000001.ldb"), []byte("chunks-"+content), 0o644)
}

func waitBackups(t *testing.T, s *Server, n int) BackupsView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		v := s.Backups()
		if !v.Running && len(v.Backups) == n {
			return v
		}
		time.Sleep(20 * time.Millisecond)
	}
	backlog, _, stop := s.Subscribe()
	stop()
	t.Fatalf("expected %d backups, have %+v\n%s", n, s.Backups(), strings.Join(backlog, "\n"))
	return BackupsView{}
}

func TestHotBackupAndRestore(t *testing.T) {
	m := newTestManager(t, newFakeMojang(t, "1.26.52.3"), 2*time.Second)
	s := createRunning(t, m, "Family")
	writeWorld(t, s, "original")

	if err := s.StartBackup(); err != nil {
		t.Fatal(err)
	}
	v := waitBackups(t, s, 1)
	b := v.Backups[0]
	if b.Kind != "manual" || b.Full || v.LastError != "" {
		t.Errorf("backup: %+v", v)
	}
	got := tarContents(t, filepath.Join(m.backupsDir(s.ID()), b.Name))
	if got["worlds/Bedrock level/db/000001.ldb"] != "chunks-original" || got["worlds/Bedrock level/level.dat"] != "level-original" {
		t.Errorf("world not in backup: %v", got)
	}
	if _, ok := got["server.properties"]; !ok {
		t.Error("server.properties not in backup")
	}
	if _, ok := got["bedrock_server"]; ok {
		t.Error("world backups shouldn't hold the server files")
	}
	waitConsole(t, s, "Backup saved")
	if consoleHas(s, "Data saved") || consoleHas(s, "000001.ldb") {
		t.Error("save query chatter should stay out of the console")
	}
	if s.Status().State != StateRunning {
		t.Error("the server should keep running during a backup")
	}

	// Change the world, then restore.
	writeWorld(t, s, "changed")
	os.WriteFile(filepath.Join(s.dir(), "worlds", "Bedrock level", "db", "000009.ldb"), []byte("new"), 0o644)
	if err := s.Restore(b.Name); err != nil {
		t.Fatal(err)
	}
	waitConsole(t, s, "Restored "+b.Name)
	waitFor(t, s, "running after restore", state(StateRunning))
	world := filepath.Join(s.dir(), "worlds", "Bedrock level")
	if readFile(t, filepath.Join(world, "db", "000001.ldb")) != "chunks-original" {
		t.Error("world not restored")
	}
	if _, err := os.Stat(filepath.Join(world, "db", "000009.ldb")); err == nil {
		t.Error("files added after the backup should be gone")
	}
	if !strings.Contains(readFile(t, filepath.Join(s.dir(), "server.properties")), "server-port=19134") {
		t.Error("panel ports not applied after restore")
	}
	// The world from before the restore was kept.
	v = waitBackups(t, s, 2)
	var safety Backup
	for _, x := range v.Backups {
		if x.Kind == "before-restore" {
			safety = x
		}
	}
	if safety.Name == "" {
		t.Fatalf("no before-restore backup: %+v", v)
	}
	if tarContents(t, filepath.Join(m.backupsDir(s.ID()), safety.Name))["worlds/Bedrock level/db/000001.ldb"] != "chunks-changed" {
		t.Error("before-restore backup doesn't hold the changed world")
	}
	leftovers, _ := filepath.Glob(filepath.Join(m.serversDir(), ".restore-*"))
	if len(leftovers) != 0 {
		t.Errorf("left behind: %v", leftovers)
	}
}

func TestHotBackupCopiesOnlyWhatTheServerListed(t *testing.T) {
	dir := t.TempDir()
	level := filepath.Join(dir, "worlds", "My World")
	os.MkdirAll(filepath.Join(level, "db"), 0o755)
	os.MkdirAll(filepath.Join(dir, "worlds", "Old World"), 0o755)
	os.WriteFile(filepath.Join(level, "db", "000005.ldb"), []byte("0123456789appended"), 0o644)
	os.WriteFile(filepath.Join(level, "db", "000006.log"), []byte("being written"), 0o644)
	os.WriteFile(filepath.Join(level, "levelname.txt"), []byte("My World"), 0o644)
	os.WriteFile(filepath.Join(dir, "worlds", "Old World", "level.dat"), []byte("old"), 0o644)
	os.WriteFile(filepath.Join(dir, "permissions.json"), []byte("[]"), 0o644)
	listed := map[string]int64{"My World/db/000005.ldb": 10, "My World/levelname.txt": 8}
	backup := func(listed map[string]int64) (string, error) {
		snap := t.TempDir()
		if err := copyWorld(context.Background(), dir, snap, listed); err != nil {
			return "", err
		}
		os.MkdirAll(filepath.Join(dir, "b"), 0o755)
		return archiveSnapshot(context.Background(), snap, filepath.Join(dir, "b"), "x", "manual")
	}
	path, err := backup(listed)
	if err != nil {
		t.Fatal(err)
	}
	got := tarContents(t, path)
	want := map[string]string{
		"worlds/My World/db/000005.ldb": "0123456789",
		"worlds/My World/levelname.txt": "My World",
		"worlds/Old World/level.dat":    "old",
		"permissions.json":              "[]",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["worlds/My World/db/000006.log"]; ok {
		t.Error("unlisted db file copied")
	}
	// A listed file shorter than listed, or missing, is an error, not a
	// quietly bad backup.
	if _, err := backup(map[string]int64{"My World/db/000005.ldb": 1000}); err == nil {
		t.Error("short file should fail")
	}
	if _, err := backup(map[string]int64{"My World/db/000099.ldb": 1}); err == nil {
		t.Error("missing file should fail")
	}
	// Whole .ldb files are hard-linked, not copied.
	snap := t.TempDir()
	if err := copyWorld(context.Background(), dir, snap, map[string]int64{"My World/db/000005.ldb": 18}); err != nil {
		t.Fatal(err)
	}
	a, _ := os.Stat(filepath.Join(level, "db", "000005.ldb"))
	b, _ := os.Stat(filepath.Join(snap, "worlds", "My World", "db", "000005.ldb"))
	if !os.SameFile(a, b) {
		t.Error("whole .ldb should be hard-linked")
	}
	// A linked world is refused rather than left out.
	os.Symlink(t.TempDir(), filepath.Join(dir, "worlds", "Linked"))
	if _, err := backup(nil); err == nil || !strings.Contains(err.Error(), "link") {
		t.Errorf("linked world: %v", err)
	}
}

func TestParseSaveList(t *testing.T) {
	got := parseSaveList("Bedrock level/db/000005.ldb:83, Bedrock level/db/CURRENT:16, Bedrock level/level.dat:2517")
	if len(got) != 3 || got["Bedrock level/level.dat"] != 2517 {
		t.Errorf("%v", got)
	}
	got = parseSaveList("My World, v2/db/000005.ldb:83, My World, v2/level.dat:10")
	if len(got) != 2 || got["My World, v2/db/000005.ldb"] != 83 {
		t.Errorf("comma in the name: %v", got)
	}
	for _, bad := range []string{"", "Saving...", "../x/y:1", "a/b:c", "a:1", "Player connected: Bob, xuid: 2535400000000000",
		"A/x:1, B/y:2", "World/x:1, trailing"} {
		if parseSaveList(bad) != nil {
			t.Errorf("%q should not parse", bad)
		}
	}
	// Only Bedrock's own log prefix is stripped, not a world called "[SMP] World".
	if reLogPrefix.ReplaceAllString("[SMP] World/level.dat:5", "") != "[SMP] World/level.dat:5" {
		t.Error("world name prefix stripped")
	}
	if reLogPrefix.ReplaceAllString("[2026-09-27 12:00:03:000 INFO] Data saved.", "") != "Data saved." {
		t.Error("log prefix not stripped")
	}
}

func TestColdBackupOfStoppedServer(t *testing.T) {
	m := newTestManager(t, newFakeMojang(t, "1.26.52.3"), 2*time.Second)
	s := createRunning(t, m, "Family")
	writeWorld(t, s, "cold")
	s.Stop()
	waitFor(t, s, "stopped", state(StateStopped))
	if err := s.StartBackup(); err != nil {
		t.Fatal(err)
	}
	v := waitBackups(t, s, 1)
	if tarContents(t, filepath.Join(m.backupsDir(s.ID()), v.Backups[0].Name))["worlds/Bedrock level/db/000001.ldb"] != "chunks-cold" {
		t.Error("world not in backup")
	}
	if s.Status().State != StateStopped {
		t.Error("a stopped server should stay stopped")
	}
}

func TestPlanDue(t *testing.T) {
	loc := time.FixedZone("MDT", -6*3600)
	at := func(s string) time.Time {
		v, _ := time.ParseInLocation("2006-01-02 15:04", s, loc)
		return v
	}
	daily := BackupPlan{Every: "daily", At: "04:00", Keep: 7}
	hourly := BackupPlan{Every: "1h", Keep: 7}
	cases := []struct {
		p         BackupPlan
		last, now string
		want      bool
	}{
		{daily, "", "2026-09-27 22:45", true}, // never backed up: straight away
		{daily, "2026-09-27 22:45", "2026-09-27 23:59", false},
		{daily, "2026-09-27 22:45", "2026-09-28 03:59", false},
		{daily, "2026-09-27 22:45", "2026-09-28 04:00", true},
		{daily, "2026-09-28 04:00", "2026-09-28 12:00", false},
		{daily, "2026-09-25 04:00", "2026-09-28 01:00", true},  // missed while off
		{hourly, "2026-09-28 04:00", "2026-09-28 04:59", true}, // within the 30s slack? no: 59 min
		{hourly, "2026-09-28 04:00", "2026-09-28 04:30", false},
		{hourly, "2026-09-28 04:00", "2026-09-28 05:00", true},
		{BackupPlan{Every: "off"}, "", "2026-09-28 05:00", false},
	}
	cases[6].want = false
	for _, c := range cases {
		var last time.Time
		if c.last != "" {
			last = at(c.last)
		}
		if got := c.p.due(last, at(c.now)); got != c.want {
			t.Errorf("%s last %q now %q: got %v", c.p.Every, c.last, c.now, got)
		}
	}
	for _, bad := range []BackupPlan{{Every: "2h", Keep: 3}, {Every: "daily", At: "25:00", Keep: 3}, {Every: "daily", At: "4am", Keep: 3}, {Every: "1h", Keep: 0}, {Every: "1h", Keep: 101}} {
		if bad.validate() == nil {
			t.Errorf("%+v should be refused", bad)
		}
	}
}

func TestScheduledBackupsSkipQuietServersAndKeepN(t *testing.T) {
	m := newTestManager(t, newFakeMojang(t, "1.26.52.3"), 2*time.Second)
	s := createRunning(t, m, "Family")
	writeWorld(t, s, "x")
	if err := s.SetPlan(BackupPlan{Every: "1h", Keep: 2}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.scheduledBackup(now) // first one always runs
	waitBackups(t, s, 1)
	s.scheduledBackup(now.Add(10 * time.Minute)) // not due
	s.scheduledBackup(now.Add(time.Hour))        // due, but nobody played
	if n := len(s.Backups().Backups); n != 1 {
		t.Fatalf("quiet server backed up again: %d", n)
	}
	for i := 2; i <= 4; i++ {
		s.Command("join Kemikal Halo")
		waitFor(t, s, "joined", func(st Status) bool { return len(st.Players) == 1 })
		s.Command("leave Kemikal Halo")
		waitFor(t, s, "left", func(st Status) bool { return len(st.Players) == 0 })
		s.scheduledBackup(now.Add(time.Duration(i) * time.Hour))
		time.Sleep(1100 * time.Millisecond) // distinct file names
	}
	v := s.Backups()
	if len(v.Backups) != 2 {
		t.Errorf("keep 2: %+v", v.Backups)
	}
	// It's remembered across restarts.
	m.StopAll()
	m2 := New(Options{DataDir: m.opts.DataDir})
	m2.Load()
	s2, _ := m2.Get(s.ID())
	if p := s2.Plan(); p.Every != "1h" || p.Keep != 2 {
		t.Errorf("plan after restart: %+v", p)
	}
	if s2.meta.LastScheduledBackup.IsZero() {
		t.Error("last scheduled backup time lost")
	}
}

func TestRestoreBeforeUpdateBackupRollsBack(t *testing.T) {
	mojang := newFakeMojang(t, "1.26.52.3")
	m := newTestManager(t, mojang, 2*time.Second)
	s := createRunning(t, m, "Family")
	writeWorld(t, s, "v1")
	mojang.release(t, "1.26.60.1")
	if err := s.Update(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, "updated", func(st Status) bool { return st.State == StateRunning && st.Version == "1.26.60.1" })
	v := s.Backups()
	if len(v.Backups) != 1 || !v.Backups[0].Full || v.Backups[0].Kind != "before-update" {
		t.Fatalf("%+v", v)
	}
	if err := s.Restore(v.Backups[0].Name); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, "back on the old version", func(st Status) bool { return st.State == StateRunning && st.Version == "1.26.52.3" })
	if readFile(t, filepath.Join(s.dir(), "worlds", "Bedrock level", "db", "000001.ldb")) != "chunks-v1" {
		t.Error("world not restored")
	}
	if _, err := os.Stat(filepath.Join(s.dir(), metaFile)); err != nil {
		t.Error("panel details lost")
	}
}

func TestBackupNamesAndUnsafeArchives(t *testing.T) {
	m := newTestManager(t, newFakeMojang(t, "1.26.52.3"), 2*time.Second)
	s := createRunning(t, m, "Family")
	for _, bad := range []string{"../x", "other-20260927-120000-manual.tar.gz", "family-20260927-120000-manual.tar.gz", "family-2026-manual.tar.gz"} {
		if _, err := s.BackupPath(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
		if s.Restore(bad) == nil || s.DeleteBackup(bad) == nil {
			t.Errorf("%q restore/delete accepted", bad)
		}
	}
	// An archive with a path out of the folder is refused.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "../evil", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	if err := extractTarGz(context.Background(), &buf, t.TempDir()); err == nil {
		t.Error("escape not refused")
	}
}

func TestPruneGoesByFileTime(t *testing.T) {
	dir := t.TempDir()
	// Names out of time order, as after a TZ change.
	names := []string{"x-20260928-170000-scheduled.tar.gz", "x-20260928-180000-scheduled.tar.gz", "x-20260928-120000-scheduled.tar.gz"}
	base := time.Now().Add(-time.Hour)
	for i, n := range names {
		p := filepath.Join(dir, n)
		os.WriteFile(p, nil, 0o644)
		os.Chtimes(p, base.Add(time.Duration(i)*time.Minute), base.Add(time.Duration(i)*time.Minute))
	}
	pruneBackups(dir, "x", "scheduled", 2)
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(left) != 2 || !strings.Contains(strings.Join(left, " "), "120000") {
		t.Errorf("the newest (by time) should stay: %v", left)
	}
}

func TestLoadFinishesInterruptedRestore(t *testing.T) {
	m := newTestManager(t, newFakeMojang(t, "1.26.52.3"), 2*time.Second)
	s := createRunning(t, m, "Family")
	writeWorld(t, s, "before")
	s.Stop()
	waitFor(t, s, "stopped", state(StateStopped))
	m.StopAll()
	// Simulate a crash in the middle of the swap: server.properties has been
	// replaced, extra.txt (new) moved in, and worlds/ moved aside but the
	// restored one not yet moved in.
	props := filepath.Join(s.dir(), "server.properties")
	origProps := readFile(t, props)
	old := filepath.Join(m.serversDir(), ".restore-old-"+s.ID())
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(old, ".swap"), []byte("1 server.properties\n0 extra.txt\n1 worlds\n"), 0o644)
	os.Rename(props, filepath.Join(old, "server.properties"))
	os.WriteFile(props, []byte("level-name=Other\n"), 0o644)
	os.WriteFile(filepath.Join(s.dir(), "extra.txt"), []byte("x"), 0o644)
	os.Rename(filepath.Join(s.dir(), "worlds"), filepath.Join(old, "worlds"))
	os.MkdirAll(filepath.Join(m.serversDir(), ".restore-new-"+s.ID(), "worlds"), 0o755)
	m2 := New(Options{DataDir: m.opts.DataDir})
	if err := m2.Load(); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(s.dir(), "worlds", "Bedrock level", "db", "000001.ldb")) != "chunks-before" {
		t.Error("world not put back")
	}
	if readFile(t, props) != origProps {
		t.Error("server.properties not undone")
	}
	if _, err := os.Stat(filepath.Join(s.dir(), "extra.txt")); err == nil {
		t.Error("file from the restore left behind")
	}
	if left, _ := filepath.Glob(filepath.Join(m.serversDir(), ".restore-*")); len(left) != 0 {
		t.Errorf("left: %v", left)
	}
	// A swap that finished is kept.
	os.MkdirAll(filepath.Join(old, "worlds"), 0o755)
	os.WriteFile(filepath.Join(old, ".swap"), []byte("1 worlds\n"), 0o644)
	os.WriteFile(filepath.Join(old, ".done"), nil, 0o644)
	New(Options{DataDir: m.opts.DataDir}).Load()
	if readFile(t, filepath.Join(s.dir(), "worlds", "Bedrock level", "db", "000001.ldb")) != "chunks-before" {
		t.Error("finished swap was undone")
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("old files left")
	}
}

func TestRestartDuringHotBackupFailsIt(t *testing.T) {
	m := newTestManager(t, newFakeMojang(t, "1.26.52.3"), 2*time.Second)
	s := createRunning(t, m, "Family")
	writeWorld(t, s, "x")
	afterHoldHook = func() error { return s.Restart() }
	defer func() { afterHoldHook = nil }()
	if err := s.StartBackup(); err != nil {
		t.Fatal(err)
	}
	v := waitBackups(t, s, 0)
	if !strings.Contains(v.LastError, "restarted") {
		t.Errorf("error: %q", v.LastError)
	}
	waitFor(t, s, "running again", state(StateRunning))
	s.mu.Lock()
	held := s.hotBackup
	s.mu.Unlock()
	if held {
		t.Error("hold flag left on")
	}
}

func TestFailedScheduledBackupIsRetried(t *testing.T) {
	m := newTestManager(t, newFakeMojang(t, "1.26.52.3"), 2*time.Second)
	s := createRunning(t, m, "Family")
	writeWorld(t, s, "x")
	s.SetPlan(BackupPlan{Every: "daily", At: "04:00", Keep: 3})
	afterHoldHook = func() error { return io.ErrShortWrite }
	now := time.Now()
	s.scheduledBackup(now)
	afterHoldHook = nil
	if !s.meta.LastScheduledBackup.IsZero() || len(s.Backups().Backups) != 0 {
		t.Fatal("a failed backup shouldn't count")
	}
	s.scheduledBackup(now.Add(time.Minute)) // too soon to retry
	if len(s.Backups().Backups) != 0 {
		t.Error("retried too soon")
	}
	s.scheduledBackup(now.Add(scheduleRetry + time.Minute))
	if len(s.Backups().Backups) != 1 {
		t.Error("not retried")
	}
}

func TestStopDuringHotBackupFailsIt(t *testing.T) {
	m := newTestManager(t, newFakeMojang(t, "1.26.52.3"), 2*time.Second)
	s := createRunning(t, m, "Family")
	writeWorld(t, s, "x")
	afterHoldHook = func() error {
		s.mu.Lock()
		s.stopping = true // as if Stop was pressed and the server is still saving
		s.mu.Unlock()
		return nil
	}
	defer func() { afterHoldHook = nil }()
	if err := s.StartBackup(); err != nil {
		t.Fatal(err)
	}
	if v := waitBackups(t, s, 0); !strings.Contains(v.LastError, "stopped") {
		t.Errorf("error: %q", v.LastError)
	}
	s.mu.Lock()
	s.stopping = false
	s.mu.Unlock()
}
