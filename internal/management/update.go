package management

import (
	"context"
	"github.com/ssfun/surge-external-bridge/internal/update"
	"net/http"
	"os"
	"strings"
)

func (s *Server) SetUpdater(manager *update.Manager, instanceID string) {
	manager.Freeze = func(work func() error) error { s.updateMu.Lock(); defer s.updateMu.Unlock(); return work() }
	s.updater = manager
	s.instanceID = instanceID
}
func (s *Server) updateGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			s.updateMu.RLock()
			defer s.updateMu.RUnlock()
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.URL.Path != "/api/session" && update.Busy(s.app.DataDir()) {
			writeError(w, http.StatusServiceUnavailable, "更新正在执行或等待恢复，暂时无法修改配置")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) hasUpdater(w http.ResponseWriter, r *http.Request) bool {
	if s.updater == nil {
		writeError(w, http.StatusServiceUnavailable, "当前运行方式未启用更新管理")
		return false
	}
	if expected := r.Header.Get("X-SurgeEB-Instance"); expected != "" && expected != s.instanceID {
		writeError(w, http.StatusConflict, "运行实例已改变，请重新执行更新命令")
		return false
	}
	return true
}
func (s *Server) updateStatus(w http.ResponseWriter, r *http.Request) {
	if !s.hasUpdater(w, r) {
		return
	}
	status := s.updater.Status()
	// Existing management APIs keep user-specific paths private on LAN access.
	if home, err := os.UserHomeDir(); err == nil {
		status.Target = strings.Replace(status.Target, home, "~", 1)
	}
	status.Error = s.publicErrorText(status.Error)
	status.CheckError = s.publicErrorText(status.CheckError)
	status.InstallReason = s.publicErrorText(status.InstallReason)
	writeJSON(w, http.StatusOK, status)
}
func (s *Server) publicErrorText(text string) string {
	return s.publicError(textError(text))
}

type textError string

func (e textError) Error() string { return string(e) }
func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	if !s.hasUpdater(w, r) {
		return
	}
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin mutation is not allowed")
		return
	}
	if err := s.updater.Check(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, s.publicError(err))
		return
	}
	s.updateStatus(w, r)
}
func (s *Server) updateInstall(w http.ResponseWriter, r *http.Request) {
	if !s.hasUpdater(w, r) {
		return
	}
	if !sameOrigin(r) || r.Header.Get("X-SurgeEB-Confirm") != "install-update" {
		writeError(w, http.StatusPreconditionFailed, "更新并重启需要显式确认")
		return
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	status := s.updater.Status()
	if !status.CanInstall {
		writeError(w, http.StatusConflict, s.publicErrorText(status.InstallReason))
		return
	}
	if err := s.updater.StartInstall(context.Background(), body.Version, true); err != nil {
		writeError(w, http.StatusConflict, s.publicError(err))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted", "version": body.Version})
}
func (s *Server) updatePreferences(w http.ResponseWriter, r *http.Request) {
	if !s.hasUpdater(w, r) {
		return
	}
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin mutation is not allowed")
		return
	}
	var prefs update.Preferences
	if err := readJSON(w, r, &prefs); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if prefs.AutoInstall && r.Header.Get("X-SurgeEB-Confirm") != "enable-auto-update" {
		writeError(w, http.StatusPreconditionFailed, "开启自动安装需要确认连接会在更新时中断")
		return
	}
	if err := s.updater.Preferences(prefs); err != nil {
		writeError(w, http.StatusBadRequest, s.publicError(err))
		return
	}
	s.updateStatus(w, r)
}
