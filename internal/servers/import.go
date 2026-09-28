package servers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Importing servers from another panel (Crafty Controller) or a plain folder.
//
// The owner mounts the other panel's server folders read-only at
// Options.ImportDir (for example /import/crafty). Scan lists the Bedrock
// servers found there; Import copies one into the panel's own storage and
// leaves the original untouched, so the old panel stays a working fallback.

// ImportCandidate is a server found in the import folder.
type ImportCandidate struct {
	Path          string `json:"path"`   // relative to the import folder
	Name          string `json:"name"`   // server-name from server.properties
	World         string `json:"world"`  // level-name
	Port          int    `json:"port"`   // server-port
	Worlds        int64  `json:"worlds"` // bytes in worlds/
	Imported      bool   `json:"imported"`
	ImportedAs    string `json:"importedAs,omitempty"`
	PortAvailable bool   `json:"portAvailable"`
}

// ImportResult is the outcome of a finished import, shown on the import
// screen.
type ImportResult struct {
	Path  string    `json:"path"`
	Name  string    `json:"name"`
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
	At    time.Time `json:"at"`
}

// ErrNoImportDir means no import folder is mounted.
var ErrNoImportDir = errors.New("no import folder is set up; mount the other panel's servers folder at /import (read-only)")

const maxScanDepth = 4

type importState struct {
	mu      sync.Mutex
	results []ImportResult
}

// ImportDir returns the folder imports are read from ("" if none).
func (m *Manager) ImportDir() string {
	if m.opts.ImportDir == "" {
		return ""
	}
	if st, err := os.Stat(m.opts.ImportDir); err != nil || !st.IsDir() {
		return ""
	}
	return m.opts.ImportDir
}

// ScanImports lists the Bedrock servers in the import folder.
func (m *Manager) ScanImports() ([]ImportCandidate, error) {
	root := m.ImportDir()
	if root == "" {
		return nil, ErrNoImportDir
	}
	imported := map[string]string{} // path -> panel name
	m.mu.Lock()
	for _, s := range m.servers {
		s.mu.Lock()
		if s.meta.ImportedFrom != "" {
			imported[s.meta.ImportedFrom] = s.meta.Name
		}
		s.mu.Unlock()
	}
	m.mu.Unlock()

	var out []ImportCandidate
	var unreadable []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Skip it, but say so: a server the panel can't read would
			// otherwise just be missing from the list.
			if rel, rerr := filepath.Rel(root, path); rerr == nil {
				unreadable = append(unreadable, filepath.ToSlash(rel))
			}
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if rel != "." && strings.Count(rel, string(os.PathSeparator))+1 > maxScanDepth {
			return filepath.SkipDir
		}
		if filepath.Base(path) == "worlds" {
			return filepath.SkipDir // don't wander through world data
		}
		if !isBedrockServer(path) {
			return nil
		}
		c := ImportCandidate{Path: filepath.ToSlash(rel)}
		if p, err := readProperties(filepath.Join(path, "server.properties")); err == nil {
			c.Name, _ = p.get("server-name")
			c.World, _ = p.get("level-name")
			if v, ok := p.get("server-port"); ok {
				c.Port, _ = strconv.Atoi(v)
			}
		}
		if c.Name == "" {
			c.Name = filepath.Base(path)
		}
		c.Worlds = dirSize(filepath.Join(path, "worlds"))
		c.ImportedAs, c.Imported = imported[c.Path]
		c.PortAvailable = m.portFree(c.Port, "")
		out = append(out, c)
		return filepath.SkipDir // a server's own folders hold no more servers
	})
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	if walkErr == nil && len(unreadable) > 0 {
		walkErr = fmt.Errorf("the panel can't read %d folder(s), such as %s; check that the files can be read by user 99 (nobody)", len(unreadable), unreadable[0])
	}
	return out, walkErr
}

func isBedrockServer(dir string) bool {
	for _, f := range []string{"server.properties", "bedrock_server"} {
		if st, err := os.Stat(filepath.Join(dir, f)); err != nil || !st.Mode().IsRegular() {
			return false
		}
	}
	return true
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// portFree reports whether a game port (and the IPv6 port after it) can be
// given to a server. except is a server ID to ignore.
func (m *Manager) portFree(port int, except string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.portFreeLocked(port, except)
}

// portFreeLocked is portFree for callers holding m.mu.
func (m *Manager) portFreeLocked(port int, except string) bool {
	if port < firstPort || port > lastPort || (port-firstPort)%2 != 0 {
		return false
	}
	for id, s := range m.servers {
		if id == except {
			continue
		}
		s.mu.Lock()
		p, p6 := s.meta.Port, s.meta.PortV6
		s.mu.Unlock()
		for _, q := range []int{port, port + 1} {
			if q == p || q == p6 {
				return false
			}
		}
	}
	return true
}

// ImportResults returns recent import outcomes, newest first.
func (m *Manager) ImportResults() []ImportResult {
	m.imports.mu.Lock()
	defer m.imports.mu.Unlock()
	out := append([]ImportResult{}, m.imports.results...)
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

func (m *Manager) noteImport(r ImportResult) {
	r.At = time.Now()
	m.imports.mu.Lock()
	defer m.imports.mu.Unlock()
	m.imports.results = append(m.imports.results, r)
	if len(m.imports.results) > 20 {
		m.imports.results = m.imports.results[len(m.imports.results)-20:]
	}
}

// Import copies a server from the import folder. relPath is a path from
// ScanImports; port 0 means pick a free one. The copy runs in the
// background; the server appears in the list straight away as "importing".
func (m *Manager) Import(relPath, name string, port int, start bool) (Status, error) {
	root := m.ImportDir()
	if root == "" {
		return Status{}, ErrNoImportDir
	}
	src, err := resolveInside(root, relPath)
	if err != nil {
		return Status{}, err
	}
	// Record the cleaned path, so "a", "a/" and "./a" count as the same
	// server for "already imported".
	absRoot, _ := filepath.Abs(root)
	cleanRel, err := filepath.Rel(absRoot, src)
	if err != nil {
		return Status{}, errors.New("bad import path")
	}
	if !isBedrockServer(src) {
		return Status{}, errors.New("that folder doesn't hold a Bedrock server")
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 40 || strings.ContainsAny(name, "\r\n\t") {
		return Status{}, errNameInvalid
	}
	rel := filepath.ToSlash(cleanRel)

	// Check the name and port and register the server under one lock, so
	// two imports can't claim the same name or port.
	m.mu.Lock()
	for _, other := range m.servers {
		other.mu.Lock()
		dupName := strings.EqualFold(other.meta.Name, name)
		from := other.meta.ImportedFrom
		other.mu.Unlock()
		if dupName {
			m.mu.Unlock()
			return Status{}, fmt.Errorf("there's already a server called %q", name)
		}
		if from == rel {
			m.mu.Unlock()
			return Status{}, errors.New("that server has already been imported")
		}
	}
	if port == 0 {
		if port, err = m.freePort(); err != nil {
			m.mu.Unlock()
			return Status{}, err
		}
	} else if !m.portFreeLocked(port, "") {
		m.mu.Unlock()
		return Status{}, fmt.Errorf("port %d can't be used: it must be an even number from %d to %d that no other server uses (each server also uses the next port up)", port, firstPort, lastPort)
	}
	id := m.newID(name)
	meta := Meta{ID: id, Name: name, Type: "bedrock", Port: port, PortV6: port + 1, Created: time.Now().UTC(), AutoStart: start, ImportedFrom: rel}
	s := newServer(m, meta)
	s.state = StateImporting
	s.busy = true
	m.servers[id] = s
	m.mu.Unlock()

	if !m.beginWork() {
		m.remove(id)
		return Status{}, errShuttingDown
	}
	m.log.Info("importing a server", "id", id, "name", name, "from", rel, "port", port)
	st := s.Status() // before the copy starts, so callers see "importing"
	go s.importFrom(src, rel, start)
	return st, nil
}

// resolveInside joins rel onto root and makes sure the result stays inside.
func resolveInside(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", errors.New("bad import path")
	}
	root, _ = filepath.Abs(root)
	p := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	// The folder itself is allowed: a single server can be mounted at /import.
	if p != root && !strings.HasPrefix(p, root+string(os.PathSeparator)) {
		return "", errors.New("bad import path")
	}
	// Refuse paths that leave the folder through a symlink, too.
	if real, err := filepath.EvalSymlinks(p); err == nil {
		realRoot, _ := filepath.EvalSymlinks(root)
		if real != realRoot && !strings.HasPrefix(real, realRoot+string(os.PathSeparator)) {
			return "", errors.New("bad import path")
		}
	}
	return p, nil
}

// copyFileHook lets tests make a copy fail.
var copyFileHook func(path string) error

func (s *Server) importFrom(src, rel string, start bool) {
	defer s.m.work.Done()
	ctx := s.m.ctx
	staging := filepath.Join(s.m.serversDir(), ".import-"+s.meta.ID)
	fail := func(err error) {
		_ = os.RemoveAll(staging)
		s.m.remove(s.meta.ID)
		s.m.noteImport(ImportResult{Path: rel, Name: s.displayName(), Error: err.Error()})
		s.m.log.Error("import failed", "id", s.meta.ID, "from", rel, "error", err)
	}

	progress := func(msg string) { // console line, and on the server's card
		s.mu.Lock()
		s.message = msg
		s.addLineLocked("[Panel] " + msg)
		s.mu.Unlock()
	}
	progress("Copying from " + rel + ". The original isn't changed.")
	size := dirSize(src)
	if err := copyTree(ctx, src, staging, size, progress); err != nil {
		fail(err)
		return
	}
	// Write the panel's details before moving the copy into place, so a
	// crash can't leave a server folder the panel doesn't know is its own.
	if err := s.writeMeta(staging); err != nil {
		fail(err)
		return
	}
	_ = os.Chmod(filepath.Join(staging, "bedrock_server"), 0o755)
	if err := os.Rename(staging, s.dir()); err != nil {
		fail(fmt.Errorf("couldn't move the copy into place: %w", err))
		return
	}
	s.mu.Lock()
	err := s.applyPanelProperties(false)
	s.busy = false
	s.state = StateStopped
	s.message = ""
	s.addLineLocked("[Panel] Import finished. Worlds, settings, allowlist and permissions were copied.")
	s.mu.Unlock()
	if err != nil {
		s.say("Couldn't set the panel's port settings: " + err.Error())
	}
	s.m.noteImport(ImportResult{Path: rel, Name: s.displayName(), OK: true})
	s.m.log.Info("import finished", "id", s.meta.ID, "from", rel)
	if start {
		if err := s.Start(); err != nil {
			s.say("Couldn't start: " + err.Error())
		}
	}
}

// copyTree copies a folder, keeping file modes. Special files are skipped, and
// so are symlinks, except that a linked world is an error. It reports progress through say.
func copyTree(ctx context.Context, src, dst string, total int64, say func(string)) error {
	var done int64
	nextReport := int64(100 << 20)
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			// Links aren't followed: they may point outside what's mounted.
			// A linked world would be silently missing, and Bedrock would
			// make a new empty one, so refuse instead.
			if rel == "worlds" || strings.HasPrefix(filepath.ToSlash(rel), "worlds/") {
				return fmt.Errorf("%s is a link to another folder, which the panel can't copy; put the real world folder there (or mount it) and try again", filepath.ToSlash(rel))
			}
			say("Skipped " + filepath.ToSlash(rel) + " (a link).")
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if copyFileHook != nil {
			if err := copyFileHook(path); err != nil {
				return err
			}
		}
		if err := copyPlain(path, target, info.Mode().Perm()|0o200); err != nil {
			return fmt.Errorf("couldn't copy %s: %w", rel, err)
		}
		done += info.Size()
		if done >= nextReport {
			if total > 0 {
				say(fmt.Sprintf("Copied %.0f of %.0f MB…", float64(done)/1e6, float64(total)/1e6))
			}
			nextReport = done + 100<<20
		}
		return nil
	})
}

func copyPlain(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
