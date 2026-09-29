package servers

import (
	"archive/zip"
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
	"time"
	"unicode"
)

// A server keeps its worlds in worlds/<folder>; server.properties' level-name
// says which one it plays. Worlds can be uploaded (a .mcworld exported from
// the game), downloaded as a .mcworld to open on a device, and switched
// between. Uploading never replaces a world: it's added next to the others.

// World is one world folder of a server.
type World struct {
	Folder   string    `json:"folder"`
	Name     string    `json:"name"` // from levelname.txt
	Current  bool      `json:"current"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Packs    int       `json:"packs"` // add-on packs in it
}

// WorldsView is the Worlds part of the Add-ons tab.
type WorldsView struct {
	Worlds  []World `json:"worlds"`
	Current string  `json:"current"`
	Running bool    `json:"running"`
}

// Worlds lists the server's worlds.
func (s *Server) Worlds() (WorldsView, error) {
	s.filesMu.Lock()
	defer s.filesMu.Unlock()
	// An unusable level-name still lists the worlds, so one can be picked.
	cur, _, err := s.currentWorld()
	if err != nil {
		cur = ""
	}
	st := s.Status()
	v := WorldsView{Current: cur, Worlds: []World{}, Running: st.State == StateRunning || st.State == StateStarting}
	entries, _ := os.ReadDir(filepath.Join(s.dir(), "worlds"))
	found := false
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		wd := filepath.Join(s.dir(), "worlds", e.Name())
		w := World{Folder: e.Name(), Current: e.Name() == cur, Size: dirSize(wd)}
		if b, err := os.ReadFile(filepath.Join(wd, "levelname.txt")); err == nil {
			w.Name = cleanText(string(b), 80)
		}
		if info, err := os.Stat(filepath.Join(wd, "level.dat")); err == nil {
			w.Modified = info.ModTime()
		} else if info, err := e.Info(); err == nil {
			w.Modified = info.ModTime()
		}
		for _, kind := range packKinds {
			w.Packs += len(installedPacks(wd)[kind])
		}
		found = found || w.Current
		v.Worlds = append(v.Worlds, w)
	}
	if !found && cur != "" {
		// Not made yet: the server makes it when it first starts.
		v.Worlds = append(v.Worlds, World{Folder: cur, Current: true})
	}
	sort.Slice(v.Worlds, func(i, j int) bool {
		if v.Worlds[i].Current != v.Worlds[j].Current {
			return v.Worlds[i].Current
		}
		return strings.ToLower(v.Worlds[i].Folder) < strings.ToLower(v.Worlds[j].Folder)
	})
	return v, nil
}

// worldFolder checks a world folder name from a request and returns its path.
func (s *Server) worldFolder(folder string) (string, error) {
	if err := worldFolderRule(folder); err != nil || folder == "" {
		return "", errors.New("there's no such world")
	}
	wd := s.worldDir(folder)
	if info, err := os.Lstat(wd); err != nil || !info.IsDir() {
		return "", errors.New("there's no such world")
	}
	return wd, nil
}

// worldFolderName turns a world's name into a folder name that the panel
// accepts and isn't taken.
func (s *Server) worldFolderName(name string) string {
	var b strings.Builder
	for _, r := range cleanText(name, 50) {
		if unicode.IsControl(r) || strings.ContainsRune(";/\\`?*<>|\":", r) {
			continue
		}
		b.WriteRune(r)
	}
	n := b.String()
	for {
		t := strings.Trim(strings.TrimSpace(n), ".")
		if t == n {
			break
		}
		n = t
	}
	if n == "" || worldFolderRule(n) != nil {
		n = "Uploaded world"
	}
	base := n
	for i := 2; ; i++ {
		if _, err := os.Lstat(s.worldDir(n)); errors.Is(err, os.ErrNotExist) {
			return n
		}
		n = base + " " + strconv.Itoa(i)
	}
}

// findWorldRoot finds the folder with level.dat in an unpacked .mcworld: the
// top, or one folder down (zips made by hand).
func findWorldRoot(dir string) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, "level.dat")); err == nil {
		return dir, nil
	}
	entries, _ := os.ReadDir(dir)
	var found []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != "__MACOSX" {
			if _, err := os.Stat(filepath.Join(dir, e.Name(), "level.dat")); err == nil {
				found = append(found, filepath.Join(dir, e.Name()))
			}
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		if len(findPackRoots(dir)) > 0 {
			return "", errors.New("that's an add-on, not a world; upload it under Add-ons instead")
		}
		return "", errors.New("there's no world in it (no level.dat)")
	}
	return "", errors.New("it has more than one world in it; upload them one at a time")
}

// UploadResult says where an uploaded world went.
type UploadResult struct {
	World   World `json:"world"`
	Playing bool  `json:"playing"` // it's now the server's world
}

// AddWorld adds the uploaded .mcworld as a new world. With play, the server
// switches to it (restarting if it's running).
func (s *Server) AddWorld(ctx context.Context, upload, fileName string, play bool) (UploadResult, error) {
	if err := s.EditBlocked(); err != nil {
		return UploadResult{}, err
	}
	staging, err := os.MkdirTemp(s.m.serversDir(), ".upload-"+s.meta.ID+"-")
	if err != nil {
		return UploadResult{}, err
	}
	defer os.RemoveAll(staging)
	if err := unzipFile(ctx, upload, staging, &unzipLimits{}); err != nil {
		return UploadResult{}, err
	}
	root, err := findWorldRoot(staging)
	if err != nil {
		return UploadResult{}, err
	}
	name := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	if b, err := os.ReadFile(filepath.Join(root, "levelname.txt")); err == nil && strings.TrimSpace(string(b)) != "" {
		name = string(b)
	}

	s.filesMu.Lock()
	if err := s.EditBlocked(); err != nil {
		s.filesMu.Unlock()
		return UploadResult{}, err
	}
	if err := os.MkdirAll(filepath.Join(s.dir(), "worlds"), 0o755); err != nil {
		s.filesMu.Unlock()
		return UploadResult{}, err
	}
	folder := s.worldFolderName(name)
	if err := os.Rename(root, s.worldDir(folder)); err != nil {
		s.filesMu.Unlock()
		return UploadResult{}, fmt.Errorf("couldn't add the world: %w", err)
	}
	s.filesMu.Unlock()

	w := World{Folder: folder, Name: cleanText(name, 80), Size: dirSize(s.worldDir(folder)), Modified: time.Now()}
	s.say("Added the world " + folder + ".")
	res := UploadResult{World: w}
	if play {
		if err := s.PlayWorld(folder); err != nil {
			return res, fmt.Errorf("the world was added, but the server couldn't switch to it: %w", err)
		}
		res.Playing = true
		res.World.Current = true
	}
	return res, nil
}

// PlayWorld makes the server play another of its worlds, restarting it if
// it's running (it returns once the server has stopped and is starting).
func (s *Server) PlayWorld(folder string) error {
	if _, err := s.worldFolder(folder); err != nil {
		return err
	}
	changes, err := s.SaveSettings(map[string]string{"level-name": folder})
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}
	s.say("The server now plays the world " + folder + ".")
	st := s.Status()
	if st.State == StateRunning || st.State == StateStarting {
		// Waits for it to stop (it saves first); starting goes on in the
		// background.
		return s.Restart()
	}
	return nil
}

// DeleteWorld removes a world the server isn't playing. A backup of just
// that world is made first ("Before deleting a world" in Backups); restoring
// it puts the world back without changing anything else.
func (s *Server) DeleteWorld(folder string) error {
	if err := s.EditBlocked(); err != nil {
		return err
	}
	check := func() (string, error) {
		wd, err := s.worldFolder(folder)
		if err != nil {
			return "", err
		}
		if cur, _, err := s.currentWorld(); err == nil && folder == cur {
			return "", errors.New("that's the world the server plays; switch to another one first")
		}
		s.mu.Lock()
		inUse := s.cmd != nil && s.playing == folder
		s.mu.Unlock()
		if inUse {
			return "", errors.New("the running server still has that world open; restart it first")
		}
		return wd, nil
	}
	// No backup copies the worlds meanwhile.
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	backupDir := s.m.backupsDir(s.meta.ID)
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return err
	}
	snap, err := os.MkdirTemp(backupDir, ".snapshot-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(snap)
	// The world isn't in use, so it's copied as it is.
	s.filesMu.Lock()
	wd, err := check()
	if err == nil {
		err = copyWorldFolder(wd, filepath.Join(snap, "worlds", folder))
	}
	s.filesMu.Unlock()
	if err != nil {
		return err
	}
	path, err := archiveSnapshot(s.m.ctx, snap, backupDir, s.meta.ID, "before-delete")
	if err != nil {
		return fmt.Errorf("couldn't back it up first, so nothing was deleted: %w", err)
	}
	pruneBackups(backupDir, s.meta.ID, "before-delete", deleteBackups)
	s.forgetLastBackup()
	s.filesMu.Lock()
	defer s.filesMu.Unlock()
	if wd, err = check(); err != nil { // again: it may have changed meanwhile
		return err
	}
	if err := os.RemoveAll(wd); err != nil {
		return err
	}
	s.say("Deleted the world " + folder + ". To get it back, restore " + filepath.Base(path) + " in Backups (nothing else changes).")
	return nil
}

// copyWorldFolder copies the plain files and folders under src to dst.
func copyWorldFolder(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			return copyFile(p, target, 0o644)
		}
		return nil
	})
}

// restoreDeletedWorld puts back a world from a "before-delete" backup, next
// to the others. The server keeps running.
func (s *Server) restoreDeletedWorld(archive *os.File) error {
	staging, err := os.MkdirTemp(s.m.serversDir(), ".upload-"+s.meta.ID+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := extractTarGz(s.m.ctx, archive, staging); err != nil {
		return fmt.Errorf("couldn't read the backup: %w", err)
	}
	entries, err := os.ReadDir(filepath.Join(staging, "worlds"))
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		return errors.New("the backup doesn't hold one world")
	}
	folder := entries[0].Name()
	if err := worldFolderRule(folder); err != nil {
		return errors.New("the backup's world has an odd name")
	}
	s.filesMu.Lock()
	defer s.filesMu.Unlock()
	if _, err := os.Lstat(s.worldDir(folder)); err == nil {
		return fmt.Errorf("there's already a world called %s; delete it (or switch away and delete it) first", folder)
	}
	if err := os.MkdirAll(filepath.Join(s.dir(), "worlds"), 0o755); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(staging, "worlds", folder), s.worldDir(folder)); err != nil {
		return err
	}
	s.say("Put back the world " + folder + ". Switch to it on the Add-ons tab to play it.")
	return nil
}

// ExportWorld writes one of the server's worlds to w as a .mcworld (a zip
// that the game opens). A running server keeps running: its saving is paused
// while the world is copied, the same as for a backup.
func (s *Server) ExportWorld(ctx context.Context, folder string, w io.Writer) error {
	if _, err := s.worldFolder(folder); err != nil {
		return err
	}
	s.backupMu.Lock()
	if err := s.canBackUp(); err != nil {
		s.backupMu.Unlock()
		return err
	}
	s.mu.Lock()
	held := s.cmd
	if held == nil {
		s.coldBackup = true // Start waits: files mustn't change mid-copy
	}
	played := s.playedSinceBackup
	s.mu.Unlock()
	snap, err := s.snapshot(ctx, held)
	s.mu.Lock()
	s.coldBackup = false
	s.playedSinceBackup = played || s.playedSinceBackup // this isn't a backup
	s.mu.Unlock()
	s.backupMu.Unlock()
	if err != nil {
		return err
	}
	// Move it out of the way of the next backup, which clears away
	// .snapshot- folders left over from interrupted ones.
	export := filepath.Join(filepath.Dir(snap), ".export"+strings.TrimPrefix(filepath.Base(snap), ".snapshot"))
	if err := os.Rename(snap, export); err != nil {
		os.RemoveAll(snap)
		return err
	}
	defer os.RemoveAll(export)
	root := filepath.Join(export, "worlds", folder)
	zw := zip.NewWriter(w)
	werr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		hdr.Method = zip.Deflate
		out, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		in.Close()
		return err
	})
	if cerr := zw.Close(); werr == nil {
		werr = cerr
	}
	return werr
}
