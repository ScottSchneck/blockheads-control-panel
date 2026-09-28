package servers

import (
	"bufio"
	"os"
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
func (p *properties) set(key, value string) {
	for i, l := range p.lines {
		if k, _, ok := splitProperty(l); ok && k == key {
			p.lines[i] = key + "=" + value
			return
		}
	}
	p.lines = append(p.lines, key+"="+value)
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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(p.lines, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
