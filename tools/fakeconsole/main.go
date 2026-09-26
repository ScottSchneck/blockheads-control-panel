// Command testclient pretends to be a console so the server list can be
// tested without real hardware. It joins over RakNet, plain-HTTP NetherNet or
// HTTPS NetherNet, waits for the menu, picks a button, and reports the
// transfer it receives. The server must run with AUTH_OFF=true, because this
// client has no Xbox account.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/df-mc/go-nethernet"
	"github.com/df-mc/go-nethernet/endpoint"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

func main() {
	mode := flag.String("mode", "raknet", "raknet | http | https")
	addr := flag.String("addr", "127.0.0.1:19132", "server list address")
	pick := flag.Int("pick", 0, "menu button to press")
	press := flag.String("press", "", "press the main-menu button whose text starts with this, instead of -pick")
	connect := flag.String("connect", "", "with -press 'Connect', the address to type into the connect form")
	port := flag.String("port", "19132", "port to type into the connect form")
	name := flag.String("name", "", "name to type into the connect form")
	save := flag.Bool("save", true, "switch on 'Save to my list' in the connect form")
	remove := flag.String("remove", "", "with -press 'Remove', remove the saved server whose button starts with this")
	expect := flag.String("expect", "", "fail unless the main menu has a button starting with this")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	d := minecraft.Dialer{}
	var conn *minecraft.Conn
	var err error
	switch *mode {
	case "raknet":
		conn, err = d.DialContext(ctx, "raknet", *addr)
	case "http", "https":
		hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
		sig := endpoint.ClientConfig{HTTPClient: hc}.New()
		n := minecraft.NetherNet{Signaling: sig, Dialer: nethernet.Dialer{}}
		conn, err = d.DialContextNetwork(ctx, n, *mode+"://"+*addr)
	default:
		fail("unknown mode %q", *mode)
	}
	if err != nil {
		fail("connect over %s: %v", *mode, err)
	}
	defer conn.Close()
	fmt.Printf("[%s] connected, spawning\n", *mode)

	if err := conn.DoSpawnContext(ctx); err != nil {
		fail("spawn: %v", err)
	}
	fmt.Printf("[%s] spawned in the menu world\n", *mode)
	_ = conn.WritePacket(&packet.SetLocalPlayerAsInitialised{EntityRuntimeID: conn.GameData().EntityRuntimeID})

	chunks, menusSeen := 0, 0
	deadline := time.Now().Add(20 * time.Second)
	_ = conn.SetReadDeadline(deadline)
	for {
		pk, err := conn.ReadPacket()
		if err != nil {
			fail("read: %v", err)
		}
		switch p := pk.(type) {
		case *packet.LevelChunk:
			chunks++
		case *packet.ModalFormRequest:
			var form struct {
				Type    string `json:"type"`
				Title   string `json:"title"`
				Content any    `json:"content"`
				Buttons []struct {
					Text string `json:"text"`
				} `json:"buttons"`
			}
			_ = json.Unmarshal(p.FormData, &form)
			var texts []string
			for _, b := range form.Buttons {
				texts = append(texts, strings.ReplaceAll(b.Text, "\n", " / "))
			}
			fmt.Printf("[%s] form %d %q (%s): %s\n", *mode, p.FormID, form.Title, form.Type, strings.Join(texts, " | "))

			var resp string
			switch {
			case form.Type == "custom_form":
				if c, ok := form.Content.([]any); ok && len(c) > 0 {
					if first, ok := c[0].(map[string]any); ok && strings.HasPrefix(fmt.Sprint(first["text"]), "§c") {
						fail("connect form refused the address: %v", first["text"])
					}
				}
				b, _ := json.Marshal([]any{nil, *connect, *port, *name, *save})
				resp = string(b)
			case form.Type == "modal":
				resp = "true"
			case p.FormID == 3: // remove list
				resp = fmt.Sprint(find(texts, *remove))
			default: // main menu
				if *expect != "" && find(texts, *expect) < 0 {
					if *remove != "" && menusSeen > 0 {
						fmt.Printf("[%s] PASS: %q is gone from the menu\n", *mode, *expect)
						return
					}
					fail("main menu has no button starting with %q", *expect)
				}
				if *remove != "" && menusSeen > 0 {
					fail("%q is still in the menu after removing it", *expect)
				}
				menusSeen++
				i := *pick
				if *press != "" {
					if i = find(texts, *press); i < 0 {
						fail("no button starting with %q", *press)
					}
				}
				resp = fmt.Sprint(i)
			}
			var r packet.ModalFormResponse
			r.FormID = p.FormID
			r.ResponseData = protocol.Option([]byte(resp))
			if err := conn.WritePacket(&r); err != nil {
				fail("answer form: %v", err)
			}
			fmt.Printf("[%s] answered %s\n", *mode, resp)
		case *packet.Transfer:
			fmt.Printf("[%s] PASS: transferred to %s port %d\n", *mode, p.Address, p.Port)
			return
		case *packet.Disconnect:
			fail("disconnected: %s", p.Message)
		}
	}
}

// find returns the index of the first text starting with prefix, or -1.
func find(texts []string, prefix string) int {
	for i, t := range texts {
		if prefix != "" && strings.HasPrefix(t, prefix) {
			return i
		}
	}
	return -1
}

func fail(format string, args ...any) {
	fmt.Printf("FAIL: "+format+"\n", args...)
	os.Exit(1)
}
