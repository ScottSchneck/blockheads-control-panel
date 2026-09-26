package serverlist

import (
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// The load timeline records the packets exchanged while a console loads into
// the menu world, with timings, and prints them as one log line. It exists to
// find out what the console waits on before the menu appears.

type timelineEntry struct {
	at  time.Duration
	src string
	id  uint32
}

type timeline struct {
	start   time.Time
	entries []timelineEntry
	stopped bool
	login   []byte // the console's Login packet payload, for the sign-in check
}

var (
	timelinesMu sync.Mutex
	timelines   = map[string]*timeline{}

	clientPacketNames = packetNames(packet.NewClientPool())
	serverPacketNames = packetNames(packet.NewServerPool())
)

const maxTimelineEntries = 200

func packetNames(pool packet.Pool) map[uint32]string {
	names := make(map[uint32]string, len(pool))
	for id, f := range pool {
		names[id] = reflect.TypeOf(f()).Elem().Name()
	}
	return names
}

func pairKey(a, b string) string {
	if a < b {
		return a + "|" + b
	}
	return b + "|" + a
}

// packetWaiters holds channels that are closed the moment a given packet
// passes in a given direction. They are used to send the empty terrain at
// exactly the right point in the loading sequence.
var packetWaiters sync.Map // waiterKey -> chan struct{}

func waiterKey(src, dst string, id uint32) string {
	return src + ">" + dst + "#" + strconv.FormatUint(uint64(id), 10)
}

// waitForPacket returns a channel closed when packet id is next sent from
// src to dst, and a function that stops waiting.
func waitForPacket(src, dst net.Addr, id uint32) (<-chan struct{}, func()) {
	key := waiterKey(src.String(), dst.String(), id)
	ch := make(chan struct{})
	packetWaiters.Store(key, ch)
	return ch, func() { packetWaiters.CompareAndDelete(key, ch) }
}

// recordPacket is the listener's PacketFunc.
func recordPacket(h packet.Header, payload []byte, src, dst net.Addr) {
	if v, ok := packetWaiters.LoadAndDelete(waiterKey(src.String(), dst.String(), h.PacketID)); ok {
		close(v.(chan struct{}))
	}
	key := pairKey(src.String(), dst.String())
	if h.PacketID == packet.IDPlayerAuthInput {
		return // sent 20 times a second; it would crowd out everything else
	}
	timelinesMu.Lock()
	defer timelinesMu.Unlock()
	t, ok := timelines[key]
	if !ok {
		if len(timelines) > 1000 { // connections that never became sessions
			timelines = map[string]*timeline{}
		}
		t = &timeline{start: time.Now()}
		timelines[key] = t
	}
	if h.PacketID == packet.IDLogin && t.login == nil {
		t.login = append([]byte(nil), payload...)
	}
	if t.stopped || len(t.entries) >= maxTimelineEntries {
		return
	}
	t.entries = append(t.entries, timelineEntry{at: time.Since(t.start), src: src.String(), id: h.PacketID})
}

// takeTimeline stops recording for a connection and returns its summary, with
// runs of the same packet collapsed ("LevelChunk x81").
func takeTimeline(local, remote net.Addr) string {
	key := pairKey(local.String(), remote.String())
	timelinesMu.Lock()
	t, ok := timelines[key]
	if !ok || t.stopped {
		timelinesMu.Unlock()
		return ""
	}
	t.stopped = true
	entries := append([]timelineEntry(nil), t.entries...)
	timelinesMu.Unlock()

	var b strings.Builder
	for i := 0; i < len(entries); {
		e := entries[i]
		j := i + 1
		for j < len(entries) && entries[j].id == e.id && entries[j].src == e.src {
			j++
		}
		fromConsole := e.src == remote.String()
		name := serverPacketNames[e.id]
		arrow := "out"
		if fromConsole {
			name = clientPacketNames[e.id]
			arrow = "in"
		}
		if name == "" {
			name = fmt.Sprintf("packet#%d", e.id)
		}
		if b.Len() > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "+%dms %s %s", e.at.Milliseconds(), arrow, name)
		if j-i > 1 {
			fmt.Fprintf(&b, " x%d", j-i)
		}
		i = j
	}
	return b.String()
}

// loginPayload returns the Login packet a console sent on this connection.
func loginPayload(local, remote net.Addr) []byte {
	timelinesMu.Lock()
	defer timelinesMu.Unlock()
	if t, ok := timelines[pairKey(local.String(), remote.String())]; ok {
		return t.login
	}
	return nil
}

func forgetTimeline(local, remote net.Addr) {
	timelinesMu.Lock()
	delete(timelines, pairKey(local.String(), remote.String()))
	timelinesMu.Unlock()
}
