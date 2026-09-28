package web

import (
	"net/http"
	"os"

	"github.com/ScottSchneck/blockheads-control-panel/internal/servers"
)

func (s *Server) backupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/servers/{id}/backups", s.withServer(s.listBackups))
	mux.HandleFunc("POST /api/servers/{id}/backups", s.withServer(s.makeBackup))
	mux.HandleFunc("PUT /api/servers/{id}/backups/plan", s.withServer(s.setBackupPlan))
	mux.HandleFunc("GET /api/servers/{id}/backups/{name}", s.withServer(s.downloadBackup))
	mux.HandleFunc("DELETE /api/servers/{id}/backups/{name}", s.withServer(s.deleteBackup))
	mux.HandleFunc("POST /api/servers/{id}/backups/{name}/restore", s.withServer(s.restoreBackup))
}

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	writeJSON(w, http.StatusOK, srv.Backups())
}

func (s *Server) makeBackup(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	if err := srv.StartBackup(); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, srv.Backups())
}

func (s *Server) setBackupPlan(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	var p servers.BackupPlan
	if !readBody(w, r, &p) {
		return
	}
	if err := srv.SetPlan(p); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, srv.Backups())
}

func (s *Server) downloadBackup(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	path, err := srv.BackupPath(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	name := r.PathValue("name") // checked by BackupPath: letters, digits and - . only
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeContent(w, r, name, st.ModTime(), f)
}

func (s *Server) deleteBackup(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	if err := srv.DeleteBackup(r.PathValue("name")); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, srv.Backups())
}

func (s *Server) restoreBackup(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	if err := srv.Restore(r.PathValue("name")); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, srv.Status())
}
