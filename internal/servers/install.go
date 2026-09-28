package servers

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DefaultDownloadAPI is Mojang's list of current server downloads.
const DefaultDownloadAPI = "https://net-secondary.web.minecraft-services.net/api/v1.0/download/links"

// Files an update never overwrites once they exist. Worlds live in worlds/,
// which the download doesn't contain.
var keepOnUpdate = map[string]bool{
	"server.properties":      true,
	"allowlist.json":         true,
	"whitelist.json":         true,
	"permissions.json":       true,
	"packetlimitconfig.json": true,
	"profanity_filter.wlist": true,
}

const maxDownload = 1 << 30 // 1 GiB; the Linux server is about 100 MB

var versionInURL = regexp.MustCompile(`bedrock-server-([0-9.]+)\.zip`)

type downloadLinks struct {
	Result struct {
		Links []struct {
			DownloadType string `json:"downloadType"`
			DownloadURL  string `json:"downloadUrl"`
		} `json:"links"`
	} `json:"result"`
}

// latestBedrock asks Mojang for the current Linux server download.
func (m *Manager) latestBedrock(ctx context.Context, preview bool) (url, version string, err error) {
	want := "serverBedrockLinux"
	if preview {
		want = "serverBedrockPreviewLinux"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.opts.DownloadAPI, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", m.userAgent())
	resp, err := m.http().Do(req)
	if err != nil {
		return "", "", fmt.Errorf("couldn't reach Mojang's download service: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("Mojang's download service answered %s", resp.Status)
	}
	var links downloadLinks
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&links); err != nil {
		return "", "", fmt.Errorf("unexpected answer from Mojang's download service: %w", err)
	}
	for _, l := range links.Result.Links {
		if l.DownloadType == want {
			v := ""
			if mm := versionInURL.FindStringSubmatch(l.DownloadURL); mm != nil {
				v = mm[1]
			}
			return l.DownloadURL, v, nil
		}
	}
	return "", "", fmt.Errorf("Mojang's download service didn't list a %s download", want)
}

func (m *Manager) userAgent() string {
	// minecraft.net turns away some non-browser clients, so look like one.
	return "Mozilla/5.0 (X11; Linux x86_64) BlockheadsControlPanel/" + m.opts.Version
}

func (m *Manager) http() *http.Client {
	if m.opts.HTTPClient != nil {
		return m.opts.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Minute}
}

// download fetches url into a file in dir and returns its path.
func (m *Manager) download(ctx context.Context, url, dir string, say func(string)) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", m.userAgent())
	resp, err := m.http().Do(req)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: %s", resp.Status)
	}
	f, err := os.CreateTemp(dir, ".download-*.zip")
	if err != nil {
		return "", err
	}
	defer f.Close()
	size := resp.ContentLength
	if size > 0 {
		say(fmt.Sprintf("Downloading %.1f MB…", float64(size)/1e6))
	} else {
		say("Downloading…")
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxDownload+1))
	if err == nil && n > maxDownload {
		err = errors.New("download is larger than expected")
	}
	if err == nil && size > 0 && n != size {
		err = fmt.Errorf("download stopped early (%d of %d bytes)", n, size)
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// extractZip unpacks a zip into dir, refusing paths that would escape it.
func extractZip(ctx context.Context, zipPath, dir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("the download isn't a valid zip: %w", err)
	}
	defer r.Close()
	root, _ := filepath.Abs(dir)
	for _, f := range r.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		target := filepath.Join(root, filepath.FromSlash(f.Name))
		if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return fmt.Errorf("the download contains an unsafe path: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !f.Mode().IsRegular() {
			continue // no symlinks or devices
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := writeZipFile(f, target); err != nil {
			return err
		}
	}
	return nil
}

func writeZipFile(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	mode := os.FileMode(0o644)
	if f.Mode()&0o111 != 0 {
		mode = 0o755
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// mergeInto copies the unpacked server from staging into dir. On an update,
// files in keepOnUpdate that already exist are left alone.
//
// It works in two steps so a failure can't leave a mix of versions: first
// every file is copied next to its target as "<name>.new" (a full disk fails
// here, and the .new files are removed); then all of them are renamed into
// place, which only fails in unusual cases and returns *partialUpdateError.
func mergeInto(ctx context.Context, staging, dir string, update bool) error {
	type pending struct{ tmp, target string }
	var todo []pending
	cleanup := func() {
		for _, p := range todo {
			os.Remove(p.tmp)
		}
	}
	err := filepath.Walk(staging, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err // still in the copy step, so nothing has changed yet
		}
		rel, _ := filepath.Rel(staging, path)
		if rel == "." {
			return nil
		}
		target := filepath.Join(dir, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if update && keepOnUpdate[filepath.ToSlash(rel)] {
			if _, err := os.Stat(target); err == nil {
				return nil
			}
		}
		tmp := target + ".new"
		todo = append(todo, pending{tmp, target})
		return copyFile(path, tmp, info.Mode().Perm())
	})
	if err != nil {
		cleanup()
		return err
	}
	for i, p := range todo {
		// Replacing a running binary's file is safe; the old one stays open.
		if err := os.Rename(p.tmp, p.target); err != nil {
			for _, rest := range todo[i:] {
				os.Remove(rest.tmp)
			}
			if i == 0 {
				return err
			}
			return &partialUpdateError{err: fmt.Errorf("only %d of %d files were replaced: %w", i, len(todo), err)}
		}
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
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
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return nil
}

// backup writes a .tar.gz of the server folder and keeps the newest keep
// backups of the same kind.
func backup(ctx context.Context, dir, backupDir, id, kind string, keep int) (string, error) {
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%s-%s.tar.gz", id, time.Now().Format("20060102-150405"), kind)
	path := filepath.Join(backupDir, name)
	f, err := os.Create(path + ".partial")
	if err != nil {
		return "", err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	walkErr := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "." || strings.HasPrefix(filepath.Base(p), ".download-") {
			return nil
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(tw, in)
		return err
	})
	for _, c := range []io.Closer{tw, gz, f} {
		if err := c.Close(); err != nil && walkErr == nil {
			walkErr = err
		}
	}
	if walkErr != nil {
		os.Remove(path + ".partial")
		return "", walkErr
	}
	if err := os.Rename(path+".partial", path); err != nil {
		return "", err
	}
	pruneBackups(backupDir, id, kind, keep)
	return path, nil
}

// pruneBackups keeps the newest keep backups of a kind. Newest by the file's
// time: the names use the panel's local time, which changes with TZ and
// daylight saving, so they don't always sort in order.
func pruneBackups(backupDir, id, kind string, keep int) {
	matches, _ := filepath.Glob(filepath.Join(backupDir, id+"-*-"+kind+".tar.gz"))
	type file struct {
		path string
		mod  time.Time
	}
	var files []file
	for _, p := range matches {
		if st, err := os.Stat(p); err == nil {
			files = append(files, file{p, st.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if !files[i].mod.Equal(files[j].mod) {
			return files[i].mod.Before(files[j].mod)
		}
		return files[i].path < files[j].path
	})
	for len(files) > keep {
		os.Remove(files[0].path)
		files = files[1:]
	}
}
