package main

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

func adminToken() string { return strings.TrimSpace(os.Getenv("ADMIN_TOKEN")) }

func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	token := adminToken()
	provided := strings.TrimSpace(r.Header.Get("X-Admin-Token"))
	if token == "" || provided == "" || subtle.ConstantTimeCompare([]byte(token), []byte(provided)) != 1 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Unauthorized"})
		return false
	}
	return true
}

func adminHandler(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		enableCORS(w)
		if r.Method == http.MethodOptions { w.WriteHeader(http.StatusNoContent); return }
		if !requireAdmin(w, r) { return }
		next(w, r)
	}
}

func adminPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }
	http.ServeFile(w, r, "admin.html")
}

func adminDevicePageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }
	http.ServeFile(w, r, "admin-device.html")
}

func adminDevicesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }
	devicesHandler(w, r)
}
func adminPairHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }
	pairHandler(w, r)
}
func adminStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }
	statusHandler(w, r)
}
func adminLogoutHandler(w http.ResponseWriter, r *http.Request) { logoutHandler(w, r) }
func adminSendHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }
	sendHandler(w, r)
}

func adminReconnectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }
	uid := getUserID(r)
	if uid == "" { w.WriteHeader(http.StatusBadRequest); _ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "user_id is required"}); return }
	s := getSession(uid)
	if s == nil || s.client == nil || !s.client.IsLoggedIn() {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "No logged-in device found", Connected: false})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.client.IsConnected() {
		if err := s.client.Connect(); err != nil { w.WriteHeader(http.StatusServiceUnavailable); _ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: err.Error(), Connected: false}); return }
	}
	_ = json.NewEncoder(w).Encode(APIResponse{Status: "success", Message: "Reconnect requested", Connected: s.client.IsConnected()})
}

func init() {
	http.HandleFunc("/admin", adminPageHandler)
	http.HandleFunc("/admin/", adminPageHandler)
	http.HandleFunc("/admin/device", adminHandler(adminDevicePageHandler))
	http.HandleFunc("/admin/devices", adminHandler(adminDevicesHandler))
	http.HandleFunc("/admin/pair", adminHandler(adminPairHandler))
	http.HandleFunc("/admin/status", adminHandler(adminStatusHandler))
	http.HandleFunc("/admin/logout", adminHandler(adminLogoutHandler))
	http.HandleFunc("/admin/reconnect", adminHandler(adminReconnectHandler))
	http.HandleFunc("/admin/test-send", adminHandler(adminSendHandler))
}
