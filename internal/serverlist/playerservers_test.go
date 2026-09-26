package serverlist

import (
	"strings"
	"testing"
)

func TestParsePlayerServer(t *testing.T) {
	redirect := []string{"geo.hivebedrock.network"}
	ok := []struct {
		address, port, name string
		want                Server
	}{
		{"play.example.com", "", "", Server{Name: "play.example.com", Address: "play.example.com", Port: 19132}},
		{" 192.168.1.50 ", "19140", "Ava", Server{Name: "Ava", Address: "192.168.1.50", Port: 19140}},
		{"play.example.com:19200", "19132", "", Server{Name: "play.example.com:19200", Address: "play.example.com", Port: 19200}},
		{"mc.example.net.", "19132", strings.Repeat("x", 40), Server{Name: strings.Repeat("x", 32), Address: "mc.example.net", Port: 19132}},
	}
	for _, c := range ok {
		got, problem := parsePlayerServer(c.address, c.port, c.name, redirect)
		if problem != "" || got != c.want {
			t.Errorf("%q %q %q: got %+v, %q; want %+v", c.address, c.port, c.name, got, problem, c.want)
		}
	}
	bad := []struct{ address, port string }{
		{"", "19132"},
		{"not a host", "19132"},
		{"bad_host.com", "19132"},
		{"-x.com", "19132"},
		{"play.example.com", "0"},
		{"play.example.com", "70000"},
		{"play.example.com", "abc"},
		{"GEO.hivebedrock.network", "19132"},
	}
	for _, c := range bad {
		if _, problem := parsePlayerServer(c.address, c.port, "", redirect); problem == "" {
			t.Errorf("%q %q should be refused", c.address, c.port)
		}
	}
}

func TestPlayerStore(t *testing.T) {
	st := newPlayerStore(t.TempDir())
	key := playerKey("2533274815313881", "Kemikal Halo")
	a := Server{Name: "A", Address: "a.example.com", Port: 19132}
	b := Server{Name: "B", Address: "b.example.com", Port: 19140}
	if err := st.add(key, "Kemikal Halo", a); err != nil {
		t.Fatal(err)
	}
	if err := st.add(key, "Kemikal Halo", b); err != nil {
		t.Fatal(err)
	}
	// Saving the same address and port again replaces it.
	renamed := Server{Name: "A2", Address: "A.example.com", Port: 19132}
	if err := st.add(key, "Kemikal Halo", renamed); err != nil {
		t.Fatal(err)
	}
	got, _ := st.list(key)
	if len(got) != 2 || got[0] != b || got[1] != renamed {
		t.Fatalf("after adds: %+v", got)
	}
	if other, _ := st.list(playerKey("", "Someone")); len(other) != 0 {
		t.Fatal("another player sees these servers")
	}
	if err := st.remove(key, b); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.list(key); len(got) != 1 || got[0] != renamed {
		t.Fatalf("after remove: %+v", got)
	}
	for i := 0; i < maxPlayerServers-1; i++ {
		if err := st.add(key, "", Server{Name: "x", Address: "h" + string(rune('a'+i%26)) + strings.Repeat("x", i/26) + ".com", Port: 19132}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.add(key, "", Server{Name: "one too many", Address: "full.example.com", Port: 19132}); err == nil {
		t.Fatal("expected the limit to apply")
	}
}
