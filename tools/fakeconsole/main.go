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

	chunks := 0
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
				Title   string `json:"title"`
				Buttons []struct {
					Text string `json:"text"`
				} `json:"buttons"`
			}
			_ = json.Unmarshal(p.FormData, &form)
			fmt.Printf("[%s] got %d empty chunks and the menu %q with buttons:", *mode, chunks, form.Title)
			for i, b := range form.Buttons {
				fmt.Printf(" [%d] %s", i, b.Text)
			}
			fmt.Println()
			resp := []byte(fmt.Sprint(*pick))
			var r packet.ModalFormResponse
			r.FormID = p.FormID
			r.ResponseData = protocol.Option(resp)
			if err := conn.WritePacket(&r); err != nil {
				fail("answer menu: %v", err)
			}
			fmt.Printf("[%s] pressed button %d\n", *mode, *pick)
		case *packet.Transfer:
			fmt.Printf("[%s] PASS: transferred to %s port %d\n", *mode, p.Address, p.Port)
			return
		case *packet.Disconnect:
			fail("disconnected: %s", p.Message)
		}
	}
}

func fail(format string, args ...any) {
	fmt.Printf("FAIL: "+format+"\n", args...)
	os.Exit(1)
}
