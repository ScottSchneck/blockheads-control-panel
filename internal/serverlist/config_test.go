package serverlist

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAllowsNetherNet(t *testing.T) {
	all := &Config{NetherNetNames: map[string]bool{}}
	if !all.allowsNetherNet("geo.hivebedrock.network:19132") {
		t.Fatal("an empty NETHERNET_NAMES should allow every name")
	}
	one := &Config{NetherNetNames: map[string]bool{"play.galaxite.net": true}}
	cases := map[string]bool{
		"play.galaxite.net":             true,
		"PLAY.Galaxite.NET:19132":       true,
		"play.galaxite.net.":            true,
		"geo.hivebedrock.network:19132": false,
		"":                              false,
	}
	for host, want := range cases {
		if got := one.allowsNetherNet(host); got != want {
			t.Errorf("allowsNetherNet(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestFilterNamesTurnsAwayOtherNames(t *testing.T) {
	cfg := &Config{NetherNetNames: map[string]bool{"play.galaxite.net": true}}
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := filterNames(cfg, inner)

	for host, want := range map[string]int{
		"play.galaxite.net:19132":       http.StatusOK,
		"geo.hivebedrock.network:19132": http.StatusNotFound,
	} {
		req := httptest.NewRequest(http.MethodGet, "http://"+host+"/v1/join", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("host %s: status %d, want %d", host, rec.Code, want)
		}
	}
}

func TestConnectionSettings(t *testing.T) {
	for _, tc := range []struct {
		env                map[string]string
		wantRak, wantNeth  bool
		wantConnection     string
		wantNetherNetNames int
	}{
		{map[string]string{}, true, false, "raknet", 0},
		{map[string]string{"CONNECTION": "nethernet"}, false, true, "nethernet", 0},
		{map[string]string{"CONNECTION": "both", "NETHERNET_NAMES": "play.galaxite.net, mco.lbsg.net"}, true, true, "both", 2},
		{map[string]string{"CONNECTION": "raknet", "NETHERNET_NAMES": "play.galaxite.net"}, true, false, "raknet", 0},
		// Settings from the first build keep working.
		{map[string]string{"SIGNALING": "off"}, true, false, "raknet", 0},
		{map[string]string{"SIGNALING": "auto"}, true, true, "both", 0},
		{map[string]string{"RAKNET": "false"}, false, true, "nethernet", 0},
	} {
		t.Setenv("LIST_IP", "10.0.1.117")
		for _, k := range []string{"CONNECTION", "NETHERNET_NAMES", "SIGNALING", "RAKNET"} {
			t.Setenv(k, "")
		}
		for k, v := range tc.env {
			t.Setenv(k, v)
		}
		c, err := LoadConfig()
		if err != nil {
			t.Fatalf("%v: %v", tc.env, err)
		}
		if c.RakNet != tc.wantRak || c.NetherNet != tc.wantNeth || c.Connection != tc.wantConnection ||
			len(c.NetherNetNames) != tc.wantNetherNetNames {
			t.Errorf("%v: got connection=%s raknet=%v nethernet=%v names=%d", tc.env, c.Connection, c.RakNet,
				c.NetherNet, len(c.NetherNetNames))
		}
	}
}

func TestExtraSignalingPorts(t *testing.T) {
	for value, want := range map[string]int{"": 2, "none": 0, "off": 0, "8080": 1} {
		t.Setenv("LIST_IP", "10.0.1.117")
		t.Setenv("EXTRA_SIGNALING_PORTS", value)
		c, err := LoadConfig()
		if err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		if got := len(c.ExtraSignalingPorts); got != want {
			t.Errorf("EXTRA_SIGNALING_PORTS=%q gave %d ports, want %d", value, got, want)
		}
	}
}
