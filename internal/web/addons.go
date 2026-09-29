package web

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ScottSchneck/blockheads-control-panel/internal/servers"
)

// Worlds and add-ons. Anyone who runs a server sees them and can switch
// worlds (as with the World folder setting); installing, changing and
// removing add-ons, uploading worlds and downloading them take someone who
// takes care of the server; deleting a world is for the owner only.

func (s *Server) addonRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/servers/{id}/packs", s.withServer(levelRun, s.listPacks))
	mux.HandleFunc("POST /api/servers/{id}/packs", s.withServer(levelCare, s.uploadPacks))
	mux.HandleFunc("POST /api/servers/{id}/packs/{uuid}", s.withServer(levelCare, s.setPack))
	mux.HandleFunc("DELETE /api/servers/{id}/packs/{uuid}", s.withServer(levelCare, s.removePack))
	mux.HandleFunc("GET /api/servers/{id}/worlds", s.withServer(levelRun, s.listWorlds))
	mux.HandleFunc("POST /api/servers/{id}/worlds", s.withServer(levelCare, s.uploadWorld))
	mux.HandleFunc("POST /api/servers/{id}/worlds/{folder}/play", s.withServer(levelRun, s.playWorld))
	mux.HandleFunc("GET /api/servers/{id}/worlds/{folder}/download", s.withServer(levelCare, s.downloadWorld))
	mux.HandleFunc("DELETE /api/servers/{id}/worlds/{folder}", s.withServer(levelOwner, s.deleteWorld))
}

const maxUpload = 4 << 30 // 4 GiB

// uploads makes sure each server takes one upload at a time.
var uploads sync.Map // server ID -> struct{}

// receive saves the request body (the uploaded file) to a temporary file.
func (s *Server) receive(w http.ResponseWriter, r *http.Request, srv *servers.Server) (path, name string, ok bool) {
	id := srv.ID()
	if _, busy := uploads.LoadOrStore(id, struct{}{}); busy {
		writeError(w, http.StatusConflict, errors.New("another upload to this server is still going; wait for it to finish"))
		return "", "", false
	}
	name, _ = url.QueryUnescape(r.Header.Get("X-File-Name"))
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	f, err := os.CreateTemp(filepath.Join(s.opts.DataDir, "servers"), ".upload-"+id+"-file-")
	if err != nil {
		uploads.Delete(id)
		writeError(w, http.StatusInternalServerError, err)
		return "", "", false
	}
	n, err := io.Copy(f, http.MaxBytesReader(w, r.Body, maxUpload))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n == 0 {
		err = errors.New("the file is empty")
	}
	if err != nil {
		os.Remove(f.Name())
		uploads.Delete(id)
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			err = errors.New("that file is bigger than 4 GB")
		}
		writeError(w, http.StatusBadRequest, err)
		return "", "", false
	}
	return f.Name(), name, true
}

func (s *Server) doneReceiving(srv *servers.Server, path string) {
	os.Remove(path)
	uploads.Delete(srv.ID())
}

func (s *Server) listPacks(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	v, err := srv.Packs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) uploadPacks(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	path, name, ok := s.receive(w, r, srv)
	if !ok {
		return
	}
	defer s.doneReceiving(srv, path)
	res, err := srv.InstallPacks(r.Context(), path)
	for _, p := range res.Installed {
		s.note(r, srv, "installed the add-on "+p.Name+" "+p.Version)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.log.Info("add-on installed", "id", srv.ID(), "file", name, "packs", len(res.Installed), "by", me(r).Name)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) setPack(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !readBody(w, r, &req) {
		return
	}
	p, err := srv.SetPackEnabled(r.PathValue("uuid"), req.Enabled)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Enabled {
		s.note(r, srv, "turned on the add-on "+p.Name)
	} else {
		s.note(r, srv, "turned off the add-on "+p.Name)
	}
	s.listPacks(w, r, srv)
}

func (s *Server) removePack(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	p, err := srv.RemovePack(r.PathValue("uuid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.note(r, srv, "removed the add-on "+p.Name)
	s.listPacks(w, r, srv)
}

func (s *Server) listWorlds(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	v, err := srv.Worlds()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) uploadWorld(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	path, name, ok := s.receive(w, r, srv)
	if !ok {
		return
	}
	defer s.doneReceiving(srv, path)
	res, err := srv.AddWorld(r.Context(), path, name, r.URL.Query().Get("play") == "1")
	if res.World.Folder != "" {
		s.note(r, srv, "uploaded the world "+res.World.Folder)
		if res.Playing {
			s.note(r, srv, "switched the server to the world "+res.World.Folder)
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) playWorld(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	folder := r.PathValue("folder")
	if err := srv.PlayWorld(folder); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, servers.ErrOutsideOpen) {
			code = http.StatusConflict
		}
		writeError(w, code, err)
		return
	}
	s.note(r, srv, "switched the server to the world "+folder)
	s.listWorlds(w, r, srv)
}

func (s *Server) downloadWorld(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	folder := r.PathValue("folder")
	fileName := strings.Map(func(c rune) rune {
		if c < 32 || c > 126 || strings.ContainsRune(`"\/;`, c) {
			return '_'
		}
		return c
	}, srv.Status().Name+" - "+folder) + ".mcworld"
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		err := srv.ExportWorld(r.Context(), folder, pw)
		pw.CloseWithError(err)
		errc <- err
	}()
	// Wait for the first bytes (or an error) before answering, so a
	// problem gets a proper error rather than a broken download.
	buf := make([]byte, 64<<10)
	n, err := io.ReadFull(pr, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		pr.Close()
		<-errc
		code := http.StatusConflict
		if strings.Contains(err.Error(), "no such world") {
			code = http.StatusNotFound
		}
		writeError(w, code, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+fileName+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf[:n])
	if _, err := io.Copy(w, pr); err != nil {
		pr.CloseWithError(err)
	}
	if err := <-errc; err != nil {
		// Too late for an error message: end the download so the browser
		// shows it failed, rather than keeping a broken world.
		s.log.Warn("world download failed", "id", srv.ID(), "world", folder, "error", err)
		panic(http.ErrAbortHandler)
	}
	s.note(r, srv, "downloaded the world "+folder)
}

func (s *Server) deleteWorld(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	folder := r.PathValue("folder")
	if err := srv.DeleteWorld(folder); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.note(r, srv, "deleted the world "+folder)
	s.listWorlds(w, r, srv)
}
