package web

import (
	"context"
	"net/http"
	"time"

	"github.com/ScottSchneck/blockheads-control-panel/internal/outside"
	"github.com/ScottSchneck/blockheads-control-panel/internal/servers"
)

// Friends outside the house. The owner sets it up (the address friends use,
// the router, consoles); anyone who takes care of a server can open it to
// them or close it.

func (s *Server) outsideRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/outside", ownerOnly(s.getOutside))
	mux.HandleFunc("PUT /api/outside", ownerOnly(s.putOutside))
	mux.HandleFunc("POST /api/outside/check", ownerOnly(s.checkOutside))
	mux.HandleFunc("GET /api/servers/{id}/outside", s.withServer(levelRun, s.serverOutside))
	mux.HandleFunc("POST /api/servers/{id}/outside", s.withServer(levelCare, s.setServerOutside))
}

// OutsideServers lists the panel's servers for the outside package.
func OutsideServers(mgr *servers.Manager) func() []outside.Server {
	return func() []outside.Server {
		var list []outside.Server
		for _, st := range mgr.List() {
			srv, ok := mgr.Get(st.ID)
			if !ok {
				continue
			}
			s := outside.Server{ID: st.ID, Name: st.Name, Port: st.Port, Open: st.Outside}
			if st.Outside {
				s.Problem = srv.OutsideProblem()
			}
			list = append(list, s)
		}
		return list
	}
}

// refresh works outside access out again, waiting a little for the answer.
func (s *Server) refreshOutside(r *http.Request) outside.Status {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	return s.out.Refresh(ctx)
}

func (s *Server) getOutside(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.out.Status())
}

func (s *Server) putOutside(w http.ResponseWriter, r *http.Request) {
	var req outside.Settings
	if !readBody(w, r, &req) {
		return
	}
	before := s.out.Settings()
	if err := s.out.SetSettings(req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	switch {
	case req.Enabled && !before.Enabled:
		s.note(r, nil, "turned on outside access for friends")
	case !req.Enabled && before.Enabled:
		s.note(r, nil, "turned off outside access for friends")
	default:
		s.note(r, nil, "changed the outside access settings")
	}
	writeJSON(w, http.StatusOK, s.refreshOutside(r))
}

func (s *Server) checkOutside(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.refreshOutside(r))
}

// serverOutsideView is what a server's page shows about friends outside.
type serverOutsideView struct {
	Open     bool   `json:"open"`    // marked open
	Reached  bool   `json:"reached"` // and nothing stops it
	Problem  string `json:"problem,omitempty"`
	Enabled  bool   `json:"enabled"` // outside access is on in the panel
	Address  string `json:"address,omitempty"`
	Port     int    `json:"port"`
	Consoles bool   `json:"consoles"`
	// ConsoleDNS is the address friends' consoles use as their DNS.
	ConsoleDNS string `json:"consoleDNS,omitempty"`
	UPnP       bool   `json:"upnp"`
	Opened     bool   `json:"opened"` // the router opened the port
	PortError  string `json:"portError,omitempty"`
	Shared     bool   `json:"shared"`
	Owner      string `json:"owner"`
}

func (s *Server) outsideView(srv *servers.Server, st outside.Status) serverOutsideView {
	ss := srv.Status()
	v := serverOutsideView{
		Open: ss.Outside, Enabled: st.Settings.Enabled, Address: st.Address, Port: ss.Port,
		Consoles: st.Settings.Consoles, UPnP: st.Settings.UPnP, Shared: st.Shared, Owner: s.auth.Username(),
	}
	// The address consoles outside use as their DNS: where the address
	// friends use leads (it's never a home address; see outside).
	v.ConsoleDNS = st.PublicIP
	if st.AddressIP != "" {
		v.ConsoleDNS = st.AddressIP
	}
	if ss.Outside {
		v.Problem = srv.OutsideProblem()
	}
	for _, x := range st.Servers {
		if x.ID == ss.ID {
			v.Reached = x.Reached && v.Problem == ""
		}
	}
	for _, p := range st.Ports {
		if p.Port.Port == ss.Port && p.Proto == "UDP" {
			v.Opened, v.PortError = p.Opened, p.Error
		}
	}
	return v
}

func (s *Server) serverOutside(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	writeJSON(w, http.StatusOK, s.outsideView(srv, s.out.Status()))
}

func (s *Server) setServerOutside(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	var req struct {
		Open bool `json:"open"`
	}
	if !readBody(w, r, &req) {
		return
	}
	was := srv.Outside()
	if err := srv.SetOutside(req.Open); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	if was != req.Open {
		if req.Open {
			s.note(r, srv, "opened the server to friends outside the house")
		} else {
			s.note(r, srv, "closed the server to friends outside the house")
		}
	}
	writeJSON(w, http.StatusOK, s.outsideView(srv, s.refreshOutside(r)))
}
