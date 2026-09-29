package servers

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Uploaded files (worlds and add-ons) are zip files made on someone's
// device, so they're unpacked carefully: nothing may land outside the
// folder, links aren't followed or made, and there are limits on size and
// number of files so a small upload can't fill the disk.

const (
	maxUnpacked      = 4 << 30 // 4 GiB in all
	maxUnpackedFiles = 100000
)

var errTooBig = errors.New("it unpacks to more than 4 GB, which is more than a world or add-on should be")

// unzipLimits tracks what's been unpacked across nested archives.
type unzipLimits struct {
	bytes int64
	files int
}

// unzipFile unpacks the zip file at path into dir.
func unzipFile(ctx context.Context, path, dir string, lim *unzipLimits) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return errors.New("it isn't a zip file (a .mcworld, .mcpack or .mcaddon is a zip file)")
	}
	defer zr.Close()
	if lim.files+len(zr.File) > maxUnpackedFiles {
		return errors.New("it has too many files")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := strings.ReplaceAll(f.Name, "\\", "/") // zips made on Windows
		clean := filepath.Clean(filepath.FromSlash(name))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || strings.Contains(name, ":") {
			return fmt.Errorf("it has an unsafe file name: %s", f.Name)
		}
		if first := strings.SplitN(filepath.ToSlash(clean), "/", 2)[0]; first == "__MACOSX" {
			continue // Mac leftovers
		}
		target := filepath.Join(dir, clean)
		mode := f.Mode()
		lim.files++ // folders count too: they use disk as well
		if lim.files > maxUnpackedFiles {
			return errors.New("it has too many files")
		}
		switch {
		case mode.IsDir() || strings.HasSuffix(name, "/"):
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		case !mode.IsRegular():
			continue // links and such
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := unzipOne(f, target, lim); err != nil {
			return err
		}
	}
	return nil
}

func unzipOne(f *zip.File, target string, lim *unzipLimits) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("couldn't read %s: %w", f.Name, err)
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	// Count what's actually written, not what the zip claims.
	n, err := io.Copy(out, io.LimitReader(rc, maxUnpacked-lim.bytes+1))
	lim.bytes += n
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("couldn't unpack %s: %w", f.Name, err)
	}
	if lim.bytes > maxUnpacked {
		return errTooBig
	}
	return nil
}
