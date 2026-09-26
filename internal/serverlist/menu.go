package serverlist

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// The console menu. The main menu lists the house servers (servers.json),
// then the player's own saved servers, then "Connect to a server…" and, when
// the player has saved servers, "Remove one of my servers".

const (
	formMain    = 1
	formConnect = 2
	formRemove  = 3
	formConfirm = 4

	// Bedrock's "form closed" reasons.
	cancelUserClosed = 0
	cancelUserBusy   = 1
)

type menuForm struct {
	Type    string       `json:"type"`
	Title   string       `json:"title"`
	Content string       `json:"content"`
	Buttons []formButton `json:"buttons"`
}

type formButton struct {
	Text  string     `json:"text"`
	Image *formImage `json:"image,omitempty"`
}

type formImage struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

type customForm struct {
	Type    string        `json:"type"`
	Title   string        `json:"title"`
	Content []formElement `json:"content"`
}

type formElement struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Placeholder string `json:"placeholder,omitempty"`
	Default     any    `json:"default,omitempty"`
}

type modalForm struct {
	Type    string `json:"type"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Button1 string `json:"button1"`
	Button2 string `json:"button2"`
}

type actionKind int

const (
	actionJoin actionKind = iota
	actionConnect
	actionRemove
)

type menuAction struct {
	kind   actionKind
	server Server
}

// connectInput is what the player typed into the connect form, kept so the
// form can be shown again with their text after a mistake.
type connectInput struct {
	address, port, name string
	save                bool
}

// menu holds one console's menu state. Its methods run from the read loop and
// from timers, so they hold mu.
type menu struct {
	mu        sync.Mutex
	cfg       *Config
	conn      *minecraft.Conn
	l         *minecraft.Listener
	log       *slog.Logger
	store     *playerStore
	key       string
	gamertag  string
	connected time.Time
	onFirst   func()

	shown   bool
	actions []menuAction
	removes []Server
	pending Server
}

func (m *menu) wasShown() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.shown
}

func (m *menu) send(id uint32, form any) bool {
	data, _ := json.Marshal(form)
	if err := m.conn.WritePacket(&packet.ModalFormRequest{FormID: id, FormData: data}); err != nil {
		m.log.Warn("could not send a menu", "error", err)
		return false
	}
	return true
}

func (m *menu) later(d time.Duration, f func()) { time.AfterFunc(d, f) }

// showMain shows the main menu. With onlyFirst it does nothing if the menu
// has already been shown.
func (m *menu) showMain(reason string, onlyFirst bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if onlyFirst && m.shown {
		return
	}
	house, err := loadServers(m.cfg.ServersFile)
	if err != nil {
		m.log.Error("could not read the server list", "error", err)
	}
	var own []Server
	if m.cfg.PlayerServers {
		if own, err = m.store.list(m.key); err != nil {
			m.log.Error("could not read saved player servers", "error", err)
		}
	}
	if len(house) == 0 && !m.cfg.PlayerServers {
		_ = m.l.Disconnect(m.conn, "No servers have been added yet. Ask the owner to add some in the panel.")
		return
	}

	form := menuForm{Type: "form", Title: m.cfg.MenuTitle, Content: "Choose where to play."}
	m.actions = m.actions[:0]
	for _, s := range house {
		b := formButton{Text: s.Name}
		if s.IconURL != "" {
			b.Image = &formImage{Type: "url", Data: s.IconURL}
		}
		form.Buttons = append(form.Buttons, b)
		m.actions = append(m.actions, menuAction{kind: actionJoin, server: s})
	}
	for _, s := range own {
		form.Buttons = append(form.Buttons, formButton{Text: s.Name + "\n§8" + hostPort(s)})
		m.actions = append(m.actions, menuAction{kind: actionJoin, server: s})
	}
	if m.cfg.PlayerServers {
		form.Buttons = append(form.Buttons, formButton{Text: "Connect to a server…"})
		m.actions = append(m.actions, menuAction{kind: actionConnect})
		if len(own) > 0 {
			form.Buttons = append(form.Buttons, formButton{Text: "Remove one of my servers"})
			m.actions = append(m.actions, menuAction{kind: actionRemove})
		}
	}
	if len(house) == 0 && len(own) == 0 {
		form.Content = "No servers have been added yet. You can connect to one by its address."
	}
	if !m.send(formMain, form) {
		return
	}
	first := !m.shown
	m.shown = true
	m.log.Info("menu shown", "reason", reason, "servers", len(house), "savedByPlayer", len(own),
		"sinceConnect", time.Since(m.connected).Round(100*time.Millisecond))
	if first && m.onFirst != nil {
		m.onFirst()
	}
}

func (m *menu) showConnect(message string, in connectInput) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if in.port == "" {
		in.port = "19132"
	}
	label := "Type the address of a Bedrock server. Ask whoever runs it if you're not sure."
	if message != "" {
		label = "§c" + message
	}
	form := customForm{Type: "custom_form", Title: "Connect to a server", Content: []formElement{
		{Type: "label", Text: label},
		{Type: "input", Text: "Server address", Placeholder: "play.example.com", Default: in.address},
		{Type: "input", Text: "Port", Placeholder: "19132", Default: in.port},
		{Type: "input", Text: "Name (optional)", Placeholder: "My friend's world", Default: in.name},
	}}
	if m.key != "" {
		form.Content = append(form.Content, formElement{Type: "toggle", Text: "Save to my list", Default: in.save})
	}
	m.send(formConnect, form)
}

func (m *menu) showRemove() {
	m.mu.Lock()
	defer m.mu.Unlock()
	own, err := m.store.list(m.key)
	if err != nil {
		m.log.Error("could not read saved player servers", "error", err)
	}
	m.removes = own
	form := menuForm{Type: "form", Title: "Remove a server", Content: "Which of your servers should be removed?"}
	for _, s := range own {
		form.Buttons = append(form.Buttons, formButton{Text: s.Name + "\n§8" + hostPort(s)})
	}
	form.Buttons = append(form.Buttons, formButton{Text: "Back"})
	m.send(formRemove, form)
}

func (m *menu) showConfirm(s Server) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending = s
	m.send(formConfirm, modalForm{
		Type:    "modal",
		Title:   "Remove " + s.Name + "?",
		Content: "It will be taken off your list. You can add it again any time.",
		Button1: "Remove",
		Button2: "Keep it",
	})
}

// handle deals with a form answer. It returns the server to send the console
// to, if the player picked one.
func (m *menu) handle(p *packet.ModalFormResponse) *Server {
	raw, ok := p.ResponseData.Value()
	if !ok || string(raw) == "null" {
		reason, _ := p.CancelReason.Value()
		switch {
		case p.FormID == formMain && reason == cancelUserBusy:
			// The console was still loading; try again shortly.
			m.later(time.Second, func() { m.showMain("retry after busy", false) })
		case p.FormID == formMain:
			m.log.Info("player closed the menu; showing it again")
			m.later(500*time.Millisecond, func() { m.showMain("reopened", false) })
		default:
			m.later(300*time.Millisecond, func() { m.showMain("back", false) })
		}
		return nil
	}

	switch p.FormID {
	case formMain:
		var index int
		if err := json.Unmarshal(raw, &index); err != nil {
			m.log.Warn("unexpected menu response", "data", string(raw))
			return nil
		}
		m.mu.Lock()
		var a menuAction
		valid := index >= 0 && index < len(m.actions)
		if valid {
			a = m.actions[index]
		}
		m.mu.Unlock()
		if !valid {
			m.showMain("list changed", false)
			return nil
		}
		switch a.kind {
		case actionConnect:
			m.showConnect("", connectInput{save: true})
		case actionRemove:
			m.showRemove()
		default:
			return &a.server
		}

	case formConnect:
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			m.log.Warn("unexpected connect form response", "data", string(raw))
			return nil
		}
		in := connectInput{
			address: formString(values, 1),
			port:    formString(values, 2),
			name:    formString(values, 3),
			save:    formBool(values, 4),
		}
		s, problem := parsePlayerServer(in.address, in.port, in.name, m.cfg.RedirectNames)
		if problem != "" {
			m.showConnect(problem, in)
			return nil
		}
		if in.save && m.key != "" {
			if err := m.store.add(m.key, m.gamertag, s); err != nil {
				m.log.Warn("could not save a player's server", "error", err)
			} else {
				m.log.Info("player saved a server", "name", s.Name, "address", s.Address, "port", s.Port)
			}
		}
		return &s

	case formRemove:
		var index int
		if err := json.Unmarshal(raw, &index); err != nil {
			return nil
		}
		m.mu.Lock()
		var s Server
		valid := index >= 0 && index < len(m.removes)
		if valid {
			s = m.removes[index]
		}
		m.mu.Unlock()
		if !valid { // "Back"
			m.showMain("back", false)
			return nil
		}
		m.showConfirm(s)

	case formConfirm:
		var yes bool
		_ = json.Unmarshal(raw, &yes)
		m.mu.Lock()
		s := m.pending
		m.mu.Unlock()
		if yes {
			if err := m.store.remove(m.key, s); err != nil {
				m.log.Warn("could not remove a player's server", "error", err)
			} else {
				m.log.Info("player removed a saved server", "name", s.Name, "address", s.Address, "port", s.Port)
			}
		}
		m.showMain("after remove", false)
	}
	return nil
}

func formString(values []json.RawMessage, i int) string {
	if i >= len(values) {
		return ""
	}
	var s string
	_ = json.Unmarshal(values[i], &s)
	return s
}

func formBool(values []json.RawMessage, i int) bool {
	if i >= len(values) {
		return false
	}
	var b bool
	_ = json.Unmarshal(values[i], &b)
	return b
}

func hostPort(s Server) string {
	if s.Port == 19132 {
		return s.Address
	}
	return s.Address + ":" + strconv.Itoa(int(s.Port))
}
