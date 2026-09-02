package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

func adminToken() string { return strings.TrimSpace(os.Getenv("ADMIN_TOKEN")) }

func checkAdminToken(r *http.Request) bool {
	token := adminToken()
	provided := strings.TrimSpace(r.Header.Get("X-Admin-Token"))
	return token != "" && provided != "" && subtle.ConstantTimeCompare([]byte(token), []byte(provided)) == 1
}

func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !checkAdminToken(r) {
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

func serveAdminPage(w http.ResponseWriter, r *http.Request, filename string) {
	if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }
	data, err := os.ReadFile(filename)
	if err != nil { http.Error(w, "Page not found", http.StatusNotFound); return }
	content := string(data)
	if !strings.Contains(content, "/admin-i18n.js") {
		content = strings.Replace(content, "</body>", `<script src="/admin-i18n.js"></script></body>`, 1)
	}
	w.Header().Set("Cache-Control", "private, max-age=30, stale-while-revalidate=60")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(content))
}

func adminI18nHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }
	w.Header().Set("Cache-Control", "public, max-age=300, stale-while-revalidate=600")
	http.ServeFile(w, r, "admin-i18n.js")
}

func adminAuthStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }
	if !checkAdminToken(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Unauthorized"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(APIResponse{Status: "success", Message: "Admin authenticated"})
}

func ensurePairLinksTable() error {
	_, err := userDB.Exec(`CREATE TABLE IF NOT EXISTS pairing_links (token TEXT PRIMARY KEY, user_id TEXT NOT NULL, created_at DATETIME NOT NULL, expires_at DATETIME NOT NULL, used INTEGER NOT NULL DEFAULT 0)`)
	return err
}

func newPairingToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil { return "", err }
	return hex.EncodeToString(b), nil
}

func adminCreatePairLinkHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { w.WriteHeader(http.StatusMethodNotAllowed); return }
	uid := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if uid == "" { w.WriteHeader(http.StatusBadRequest); _ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "user_id is required"}); return }
	if err := ensurePairLinksTable(); err != nil { w.WriteHeader(http.StatusInternalServerError); _ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Could not initialize pairing links"}); return }
	token, err := newPairingToken()
	if err != nil { w.WriteHeader(http.StatusInternalServerError); _ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Could not create pairing link"}); return }
	now := time.Now().UTC()
	expires := now.Add(24 * time.Hour)
	_, err = userDB.Exec(`INSERT INTO pairing_links(token,user_id,created_at,expires_at,used) VALUES(?,?,?,?,0)`, token, uid, now, expires)
	if err != nil { w.WriteHeader(http.StatusInternalServerError); _ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Could not save pairing link"}); return }
	base := strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/")
	if base == "" { base = "https://whatsapp-custom-1.onrender.com" }
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status":"success","user_id":uid,"link":base+"/connect/"+token,"expires_at":expires.Format(time.RFC3339)})
}

func getPairLink(token string) (string, bool) {
	if strings.TrimSpace(token) == "" || ensurePairLinksTable() != nil { return "", false }
	var uid string
	var expires time.Time
	var used int
	err := userDB.QueryRow(`SELECT user_id,expires_at,used FROM pairing_links WHERE token=?`, token).Scan(&uid, &expires, &used)
	if err != nil || used != 0 || time.Now().UTC().After(expires) { return "", false }
	return uid, true
}

func publicPairPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }
	token := strings.TrimPrefix(r.URL.Path, "/connect/")
	if _, ok := getPairLink(token); !ok { http.Error(w, "This pairing link is invalid or expired.", http.StatusNotFound); return }
	data, err := os.ReadFile("public-pairing.html")
	if err != nil { http.Error(w, "Page not found", http.StatusNotFound); return }
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func publicPairStartHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost { w.WriteHeader(http.StatusMethodNotAllowed); return }
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	uid, ok := getPairLink(token)
	if !ok { w.WriteHeader(http.StatusNotFound); _ = json.NewEncoder(w).Encode(APIResponse{Status:"error", Message:"Pairing link is invalid or expired"}); return }
	phone := strings.TrimSpace(r.URL.Query().Get("phone"))
	if phone == "" { w.WriteHeader(http.StatusBadRequest); _ = json.NewEncoder(w).Encode(APIResponse{Status:"error", Message:"Phone number is required"}); return }
	if s := getSession(uid); s != nil {
		if s.client != nil && s.client.IsLoggedIn() { _ = json.NewEncoder(w).Encode(APIResponse{Status:"error", Message:"This WhatsApp account is already connected", Connected:true}); return }
		_ = json.NewEncoder(w).Encode(APIResponse{Status:"error", Message:"Pairing is already in progress"}); return
	}
	device := waContainer.NewDevice()
	client := whatsmeow.NewClient(device, waLog.Stdout("Client-"+uid, "INFO", true))
	if err := client.Connect(); err != nil { w.WriteHeader(http.StatusServiceUnavailable); _ = json.NewEncoder(w).Encode(APIResponse{Status:"error", Message:err.Error()}); return }
	s := &Session{client:client}
	if !createPendingSession(uid, s) { _ = json.NewEncoder(w).Encode(APIResponse{Status:"error", Message:"Pairing already in progress"}); return }
	code, err := client.PairPhone(context.Background(), phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil { removeSession(uid); _ = json.NewEncoder(w).Encode(APIResponse{Status:"error", Message:err.Error(), Connected:client.IsConnected()}); return }
	_ = json.NewEncoder(w).Encode(map[string]any{"status":"success","code":code,"message":"Pairing started. Enter this code in WhatsApp.","connected":client.IsConnected()})
}

func publicPairStatusHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	uid, ok := getPairLink(token)
	if !ok { w.WriteHeader(http.StatusNotFound); _ = json.NewEncoder(w).Encode(APIResponse{Status:"error", Message:"Pairing link is invalid or expired"}); return }
	s := getSession(uid)
	state := "waiting"
	connected, loggedIn := false, false
	if s != nil && s.client != nil {
		loggedIn = s.client.IsLoggedIn(); connected = s.client.IsConnected()
		switch { case loggedIn && connected: state="ready"; case connected: state="connected"; case loggedIn: state="logged_in"; default: state="disconnected" }
	}
	if loggedIn && s.client.Store != nil && s.client.Store.ID != nil {
		_ = saveUserSession(uid, *s.client.Store.ID)
		_, _ = userDB.Exec(`UPDATE pairing_links SET used=1 WHERE token=?`, token)
		setActive(uid, s)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status":"success","state":state,"connected":connected,"logged_in":loggedIn})
}

func adminPageHandler(w http.ResponseWriter, r *http.Request) { serveAdminPage(w, r, "admin.html") }
func adminDevicePageHandler(w http.ResponseWriter, r *http.Request) { serveAdminPage(w, r, "admin-device.html") }
func adminDevicesPageHandler(w http.ResponseWriter, r *http.Request) { serveAdminPage(w, r, "admin-devices.html") }
func adminPairPageHandler(w http.ResponseWriter, r *http.Request) { serveAdminPage(w, r, "admin-pair.html") }
func adminSettingsPageHandler(w http.ResponseWriter, r *http.Request) { serveAdminPage(w, r, "admin-settings.html") }

func adminDevicesHandler(w http.ResponseWriter, r *http.Request) { if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }; devicesHandler(w, r) }
func adminPairHandler(w http.ResponseWriter, r *http.Request) { if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }; pairHandler(w, r) }
func adminStatusHandler(w http.ResponseWriter, r *http.Request) { if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }; statusHandler(w, r) }
func adminLogoutHandler(w http.ResponseWriter, r *http.Request) { logoutHandler(w, r) }
func adminSendHandler(w http.ResponseWriter, r *http.Request) { if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }; sendHandler(w, r) }

func adminReconnectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }
	uid := getUserID(r)
	if uid == "" { w.WriteHeader(http.StatusBadRequest); _ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "user_id is required"}); return }
	s := getSession(uid)
	if s == nil || s.client == nil || !s.client.IsLoggedIn() { w.WriteHeader(http.StatusNotFound); _ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "No logged-in device found", Connected: false}); return }
	s.mu.Lock(); defer s.mu.Unlock()
	if !s.client.IsConnected() { if err := s.client.Connect(); err != nil { w.WriteHeader(http.StatusServiceUnavailable); _ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: err.Error(), Connected: false}); return } }
	_ = json.NewEncoder(w).Encode(APIResponse{Status: "success", Message: "Reconnect requested", Connected: s.client.IsConnected()})
}

func init() {
	http.HandleFunc("/admin", adminPageHandler)
	http.HandleFunc("/admin/", adminPageHandler)
	http.HandleFunc("/admin-i18n.js", adminI18nHandler)
	http.HandleFunc("/admin/device", adminDevicePageHandler)
	http.HandleFunc("/admin/devices", adminDevicesPageHandler)
	http.HandleFunc("/admin/pair", adminPairPageHandler)
	http.HandleFunc("/admin/settings", adminSettingsPageHandler)
	http.HandleFunc("/admin/auth", adminAuthStatusHandler)
	http.HandleFunc("/admin/devices/data", adminHandler(adminDevicesHandler))
	http.HandleFunc("/admin/pair/data", adminHandler(adminPairHandler))
	http.HandleFunc("/admin/pair-link", adminHandler(adminCreatePairLinkHandler))
	http.HandleFunc("/admin/status", adminHandler(adminStatusHandler))
	http.HandleFunc("/admin/logout", adminHandler(adminLogoutHandler))
	http.HandleFunc("/admin/reconnect", adminHandler(adminReconnectHandler))
	http.HandleFunc("/admin/test-send", adminHandler(adminSendHandler))
	http.HandleFunc("/connect/", publicPairPageHandler)
	http.HandleFunc("/connect/pair", publicPairStartHandler)
	http.HandleFunc("/connect/status", publicPairStatusHandler)
}