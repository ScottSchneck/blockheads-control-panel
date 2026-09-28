package web

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/ScottSchneck/blockheads-control-panel/internal/activity"
	"github.com/ScottSchneck/blockheads-control-panel/internal/auth"
)

// People are the other accounts (kids, usually). Only the owner manages them.

func (s *Server) peopleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/users", ownerOnly(s.listUsers))
	mux.HandleFunc("POST /api/users", ownerOnly(s.createUser))
	mux.HandleFunc("PUT /api/users/{name}", ownerOnly(s.changeUser))
	mux.HandleFunc("POST /api/users/{name}/password", ownerOnly(s.setUserPassword))
	mux.HandleFunc("DELETE /api/users/{name}", ownerOnly(s.deleteUser))
	mux.HandleFunc("GET /api/activity", s.listActivity)
}

type serverName struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	list := s.mgr.List()
	known := map[string]bool{}
	names := []serverName{}
	for _, st := range list {
		known[st.ID] = true
		names = append(names, serverName{st.ID, st.Name})
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i].Name) < strings.ToLower(names[j].Name) })
	users := s.auth.Users()
	for _, u := range users {
		for id := range u.Grants {
			if !known[id] {
				delete(u.Grants, id) // a server that's gone
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users, "servers": names})
}

func userError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrNoUser):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, auth.ErrBusy):
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, err)
	default:
		writeError(w, http.StatusBadRequest, err)
	}
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		CanAdd   bool   `json:"canAdd"`
	}
	if !readBody(w, r, &req) {
		return
	}
	if err := s.auth.CreateUser(r.Context(), req.Username, req.Password, req.CanAdd); err != nil {
		userError(w, err)
		return
	}
	name, _ := s.auth.IsAccountName(req.Username)
	s.note(r, nil, "made an account for "+name)
	s.log.Info("account created", "username", name, "by", me(r).Name)
	writeJSON(w, http.StatusCreated, map[string]string{"username": name})
}

func (s *Server) changeUser(w http.ResponseWriter, r *http.Request) {
	// Only what's sent changes: canAdd if present, and the servers named
	// in grants ("" takes someone off a server).
	var req struct {
		CanAdd *bool             `json:"canAdd"`
		Grants map[string]string `json:"grants"`
	}
	if !readBody(w, r, &req) {
		return
	}
	for id, role := range req.Grants {
		if _, ok := s.mgr.Get(id); !ok && role != "" {
			writeError(w, http.StatusBadRequest, errors.New("no server with the ID "+strconv.Quote(id)))
			return
		}
	}
	name := r.PathValue("name")
	before := s.userByName(name)
	if err := s.auth.SetUser(name, req.CanAdd, req.Grants); err != nil {
		userError(w, err)
		return
	}
	if before != nil {
		s.noteChanges(r, *before, req.CanAdd, req.Grants)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) userByName(name string) *auth.User {
	for _, u := range s.auth.Users() {
		if strings.EqualFold(u.Username, strings.TrimSpace(name)) {
			return &u
		}
	}
	return nil
}

// roleWords finish "Scott let Ava ..." in the activity log.
var roleWords = map[string]string{
	auth.RoleStart: " turn it on",
	auth.RoleRun:   " run it",
	auth.RoleCare:  " take care of it",
}

// noteChanges writes what the owner changed about someone to the activity log.
//
// These are logged without a server, so only the owner sees them: kids
// don't need to see who else has which server.
func (s *Server) noteChanges(r *http.Request, before auth.User, canAdd *bool, grants map[string]string) {
	if canAdd != nil && before.CanAdd != *canAdd {
		if *canAdd {
			s.note(r, nil, "let "+before.Username+" add servers")
		} else {
			s.note(r, nil, "stopped "+before.Username+" adding servers")
		}
	}
	for id, now := range grants {
		if before.Grants[id] == now {
			continue
		}
		srv, ok := s.mgr.Get(id)
		if !ok {
			continue
		}
		name := srv.Status().Name
		if now == "" {
			s.note(r, nil, "took "+before.Username+" off "+name)
		} else {
			s.note(r, nil, "let "+before.Username+roleWords[now]+" ("+name+")")
		}
	}
}

func (s *Server) setUserPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !readBody(w, r, &req) {
		return
	}
	name := r.PathValue("name")
	if err := s.auth.SetUserPassword(r.Context(), name, req.Password); err != nil {
		userError(w, err)
		return
	}
	s.note(r, nil, "gave "+name+" a new password")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	u := s.userByName(name)
	if err := s.auth.DeleteUser(name); err != nil {
		userError(w, err)
		return
	}
	if u != nil {
		name = u.Username
	}
	s.note(r, nil, "removed "+name+"'s account")
	s.log.Info("account removed", "username", name, "by", me(r).Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// listActivity shows what happened: everything to the owner; to anyone else,
// what happened on servers they run or take care of.
func (s *Server) listActivity(w http.ResponseWriter, r *http.Request) {
	p := me(r)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	server := r.URL.Query().Get("server")
	list := s.activity.List(limit, func(e activity.Entry) bool {
		if server != "" && e.ServerID != server {
			return false
		}
		if p.Owner {
			return true
		}
		return e.ServerID != "" && p.Level(e.ServerID) >= levelRun
	})
	writeJSON(w, http.StatusOK, list)
}
