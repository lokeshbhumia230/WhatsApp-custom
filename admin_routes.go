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
	data, err := os.ReadFile("admin.html")
	if err != nil { http.Error(w, "Admin page unavailable", http.StatusInternalServerError); return }
	page := string(data)
	injection := `<style>.global-admin-menu{position:fixed;top:18px;right:18px;z-index:9999}.global-admin-dots{width:42px;height:42px;border:1px solid #d8d8d8;border-radius:10px;background:#fff;color:#111;font-size:24px;line-height:1;cursor:pointer;box-shadow:0 3px 12px #0001}.global-admin-panel{display:none;position:absolute;right:0;top:49px;width:210px;padding:7px;background:#fff;border:1px solid #ddd;border-radius:12px;box-shadow:0 15px 35px #0002}.global-admin-menu.open .global-admin-panel{display:block}.global-admin-panel button{display:block;width:100%;text-align:left;background:#fff;color:#111;border:0;padding:11px 12px;border-radius:8px;font-weight:650;cursor:pointer}.global-admin-panel button:hover{background:#f4f4f4}.global-admin-panel .danger{color:#b00000}</style><div class="global-admin-menu" id="globalAdminMenu"><button class="global-admin-dots" onclick="toggleGlobalAdminMenu(event)" aria-label="Admin navigation">⋮</button><div class="global-admin-panel"><button onclick="location.href='/admin'">Dashboard</button><button onclick="location.href='/admin#devices'">Devices</button><button onclick="location.href='/admin#pair'">Pair WhatsApp</button><button onclick="location.href='/admin/settings'">Settings</button><button class="danger" onclick="sessionStorage.removeItem('admin_token');location.reload()">Logout</button></div></div><script>function toggleGlobalAdminMenu(e){e.stopPropagation();document.getElementById('globalAdminMenu').classList.toggle('open')}document.addEventListener('click',function(e){if(!e.target.closest('#globalAdminMenu'))document.getElementById('globalAdminMenu').classList.remove('open')});</script>`
	page = strings.Replace(page, "</body>", injection+"</body>", 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(page))
}

func adminDevicePageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }
	http.ServeFile(w, r, "admin-device.html")
}

func adminSettingsPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }
	http.ServeFile(w, r, "admin-settings.html")
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
	http.HandleFunc("/admin/settings", adminHandler(adminSettingsPageHandler))
	http.HandleFunc("/admin/devices", adminHandler(adminDevicesHandler))
	http.HandleFunc("/admin/pair", adminHandler(adminPairHandler))
	http.HandleFunc("/admin/status", adminHandler(adminStatusHandler))
	http.HandleFunc("/admin/logout", adminHandler(adminLogoutHandler))
	http.HandleFunc("/admin/reconnect", adminHandler(adminReconnectHandler))
	http.HandleFunc("/admin/test-send", adminHandler(adminSendHandler))
}
