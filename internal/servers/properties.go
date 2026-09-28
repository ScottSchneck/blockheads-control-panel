package servers

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// properties reads and edits a server.properties file while keeping its
// comments, order and any keys the panel doesn't know about. Only lines whose
// value changes are rewritten.
type properties struct {
	lines []string
}

func readProperties(path string) (*properties, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	p := &properties{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		p.lines = append(p.lines, sc.Text())
	}
	return p, sc.Err()
}

func splitProperty(line string) (key, value string, ok bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "!") {
		return "", "", false
	}
	i := strings.IndexAny(t, "=:")
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(t[:i]), strings.TrimSpace(t[i+1:]), true
}

// get returns a key's value.
func (p *properties) get(key string) (string, bool) {
	for _, l := range p.lines {
		if k, v, ok := splitProperty(l); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// set changes a key's value, adding the key at the end if it's missing.
// set changes a key's value, adding the key at the end if it's missing. If
// the key appears more than once, the extra lines are removed, so there's
// no doubt which value Bedrock uses.
func (p *properties) set(key, value string) {
	found := false
	kept := p.lines[:0]
	for _, l := range p.lines {
		if k, _, ok := splitProperty(l); ok && k == key {
			if found {
				continue
			}
			found = true
			l = key + "=" + value
		}
		kept = append(kept, l)
	}
	p.lines = kept
	if !found {
		p.lines = append(p.lines, key+"="+value)
	}
}

// duplicates returns keys that appear more than once.
func (p *properties) duplicates() []string {
	seen := map[string]int{}
	var dups []string
	for _, l := range p.lines {
		if k, _, ok := splitProperty(l); ok {
			seen[k]++
			if seen[k] == 2 {
				dups = append(dups, k)
			}
		}
	}
	return dups
}

// setIfPresent changes a key only if the file already has it, for settings
// that only some server versions understand.
func (p *properties) setIfPresent(key, value string) bool {
	if _, ok := p.get(key); !ok {
		return false
	}
	p.set(key, value)
	return true
}

func (p *properties) write(path string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".server.properties-*")
	if err != nil {
		return err
	}
	_, err = f.WriteString(strings.Join(p.lines, "\n") + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o644)
	}
	if err != nil {
		os.Remove(f.Name())
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		os.Remove(f.Name())
		return err
	}
	return nil
}
