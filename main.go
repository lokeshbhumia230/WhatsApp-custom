package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	waProto "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

type Session struct {
	client *whatsmeow.Client
	mu     sync.Mutex
}

type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	pending  map[string]*Session
}

var (
	waContainer *sqlstore.Container
	userDB      *sql.DB
	manager     = &SessionManager{sessions: make(map[string]*Session), pending: make(map[string]*Session)}
)

type APIResponse struct {
	Status    string `json:"status"`
	Message   string `json:"message,omitempty"`
	Code      string `json:"code,omitempty"`
	Connected bool   `json:"connected"`
}

type StatusResponse struct {
	UserID     string `json:"user_id"`
	LoggedIn   bool   `json:"logged_in"`
	Connected  bool   `json:"connected"`
	State      string `json:"state"`
	ServerTime string `json:"server_time"`
}

type DeviceInfo struct {
	UserID    string `json:"user_id"`
	Phone     string `json:"phone,omitempty"`
	Connected bool   `json:"connected"`
	LoggedIn  bool   `json:"logged_in"`
	State     string `json:"state"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

func enableCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-User-ID, X-Admin-Token")
}

func getUserID(r *http.Request) string {
	id := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if id == "" {
		id = strings.TrimSpace(r.Header.Get("X-User-ID"))
	}
	return id
}

func requireUserID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := getUserID(r)
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "user_id is required"})
		return "", false
	}
	return id, true
}

func getSession(userID string) *Session {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if s := manager.sessions[userID]; s != nil {
		return s
	}
	return manager.pending[userID]
}

func createPendingSession(userID string, s *Session) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.sessions[userID] != nil || manager.pending[userID] != nil {
		return false
	}
	manager.pending[userID] = s
	return true
}

func setActive(userID string, s *Session) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	delete(manager.pending, userID)
	manager.sessions[userID] = s
}

func removeSession(userID string) *Session {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	s := manager.sessions[userID]
	delete(manager.sessions, userID)
	delete(manager.pending, userID)
	return s
}

func saveUserSession(userID string, jid types.JID) error {
	_, err := userDB.Exec(`INSERT INTO user_sessions(user_id,jid,updated_at) VALUES(?,?,?) ON CONFLICT(user_id) DO UPDATE SET jid=excluded.jid,updated_at=excluded.updated_at`, userID, jid.String(), time.Now().UTC())
	return err
}

func deleteUserSession(userID string) error {
	_, err := userDB.Exec(`DELETE FROM user_sessions WHERE user_id=?`, userID)
	return err
}

func loadSessions(ctx context.Context) error {
	rows, err := userDB.Query(`SELECT user_id,jid FROM user_sessions`)
	if err != nil {
		return err
	}
	defer rows.Close()

	devices, err := waContainer.GetAllDevices(ctx)
	if err != nil {
		return err
	}
	byJID := make(map[string]*types.JID)
	for _, device := range devices {
		if device.ID != nil {
			jid := *device.ID
			byJID[jid.String()] = device.ID
		}
	}

	for rows.Next() {
		var uid, jidString string
		if err := rows.Scan(&uid, &jidString); err != nil {
			return err
		}
		jid, err := types.ParseJID(jidString)
		if err != nil {
			_ = deleteUserSession(uid)
			continue
		}
		if _, ok := byJID[jid.String()]; !ok {
			_ = deleteUserSession(uid)
			continue
		}
		device, err := waContainer.GetDevice(ctx, jid)
		if err != nil || device == nil {
			_ = deleteUserSession(uid)
			continue
		}
		client := whatsmeow.NewClient(device, waLog.Stdout("Client-"+uid, "INFO", true))
		if err := client.Connect(); err != nil {
			continue
		}
		manager.mu.Lock()
		manager.sessions[uid] = &Session{client: client}
		manager.mu.Unlock()
	}
	return rows.Err()
}

func main() {
	ctx := context.Background()
	dbLog := waLog.Stdout("Database", "WARN", true)

	var err error
	waContainer, err = sqlstore.New(ctx, "sqlite3", "file:store.db?_foreign_keys=on", dbLog)
	if err != nil {
		panic(err)
	}

	userDB, err = sql.Open("sqlite3", "file:users.db?_foreign_keys=on")
	if err != nil {
		panic(err)
	}
	if _, err = userDB.Exec(`CREATE TABLE IF NOT EXISTS user_sessions (user_id TEXT PRIMARY KEY, jid TEXT NOT NULL UNIQUE, updated_at DATETIME NOT NULL)`); err != nil {
		panic(err)
	}
	if err = loadSessions(ctx); err != nil {
		panic(err)
	}

	http.HandleFunc("/", rootHandler)
	http.HandleFunc("/pairing", pairingPageHandler)
	http.HandleFunc("/pair", pairHandler)
	http.HandleFunc("/send", sendHandler)
	http.HandleFunc("/status", statusHandler)
	http.HandleFunc("/devices", devicesHandler)
	http.HandleFunc("/logout", logoutHandler)

	fmt.Println("Multi-user WhatsApp API Server running on port 3000...")
	if err = http.ListenAndServe(":3000", nil); err != nil {
		panic(err)
	}
}

func pairingPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	http.ServeFile(w, r, "pairing.html")
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	uid, ok := requireUserID(w, r)
	if !ok {
		return
	}

	s := getSession(uid)
	response := StatusResponse{UserID: uid, State: "not_found", ServerTime: time.Now().UTC().Format(time.RFC3339Nano)}
	if s != nil && s.client != nil {
		response.LoggedIn = s.client.IsLoggedIn()
		response.Connected = s.client.IsConnected()
		switch {
		case response.LoggedIn && response.Connected:
			response.State = "ready"
		case response.Connected:
			response.State = "connected"
		case response.LoggedIn:
			response.State = "logged_in"
		default:
			response.State = "disconnected"
		}

		if response.LoggedIn && s.client.Store != nil && s.client.Store.ID != nil {
			if err := saveUserSession(uid, *s.client.Store.ID); err == nil {
				setActive(uid, s)
			}
		}
	}

	statusCode := http.StatusServiceUnavailable
	if response.Connected {
		statusCode = http.StatusOK
	}
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(response)
}

func devicesHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	rows, err := userDB.Query(`SELECT user_id,jid,updated_at FROM user_sessions ORDER BY updated_at DESC`)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: err.Error()})
		return
	}
	defer rows.Close()

	devices := make([]DeviceInfo, 0)
	for rows.Next() {
		var uid, jidString string
		var updated time.Time
		if err := rows.Scan(&uid, &jidString, &updated); err != nil {
			continue
		}
		info := DeviceInfo{UserID: uid, UpdatedAt: updated.UTC().Format(time.RFC3339)}
		if jid, err := types.ParseJID(jidString); err == nil {
			info.Phone = jid.User
		}
		if s := getSession(uid); s != nil && s.client != nil {
			info.LoggedIn = s.client.IsLoggedIn()
			info.Connected = s.client.IsConnected()
			switch {
			case info.LoggedIn && info.Connected:
				info.State = "ready"
			case info.Connected:
				info.State = "connected"
			case info.LoggedIn:
				info.State = "logged_in"
			default:
				info.State = "disconnected"
			}
		} else {
			info.State = "offline"
		}
		devices = append(devices, info)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "devices": devices})
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	uid, ok := requireUserID(w, r)
	if !ok {
		return
	}

	s := getSession(uid)
	connected := s != nil && s.client != nil && s.client.IsConnected()
	loggedIn := s != nil && s.client != nil && s.client.IsLoggedIn()
	status := "Not Logged In"
	if loggedIn && connected {
		status = "Logged In & Ready"
	} else if loggedIn {
		status = "Logged In but Disconnected"
	}
	_ = json.NewEncoder(w).Encode(APIResponse{Status: "success", Message: "API is running. State: " + status, Connected: connected})
}

func pairHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	uid, ok := requireUserID(w, r)
	if !ok {
		return
	}
	phone := strings.TrimSpace(r.URL.Query().Get("phone"))
	if phone == "" {
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Invalid phone"})
		return
	}

	if existing := getSession(uid); existing != nil {
		if existing.client != nil && existing.client.IsLoggedIn() {
			_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Already paired! Use /logout to reset.", Connected: existing.client.IsConnected()})
			return
		}
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Pairing already in progress"})
		return
	}

	device := waContainer.NewDevice()
	client := whatsmeow.NewClient(device, waLog.Stdout("Client-"+uid, "INFO", true))
	if err := client.Connect(); err != nil {
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: err.Error()})
		return
	}
	s := &Session{client: client}
	if !createPendingSession(uid, s) {
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Pairing already in progress"})
		return
	}

	code, err := client.PairPhone(context.Background(), phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		removeSession(uid)
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: err.Error(), Connected: client.IsConnected()})
		return
	}
	_ = json.NewEncoder(w).Encode(APIResponse{Status: "success", Code: code, Connected: client.IsConnected(), Message: "Pairing started. Poll /status with the same user_id."})
}

func sendHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	uid, ok := requireUserID(w, r)
	if !ok {
		return
	}

	s := getSession(uid)
	if s == nil || s.client == nil || !s.client.IsLoggedIn() || !s.client.IsConnected() {
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Bot is not connected", Connected: false})
		return
	}
	phone := strings.TrimSpace(r.URL.Query().Get("phone"))
	text := r.URL.Query().Get("text")
	if phone == "" || text == "" {
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Phone and text are required", Connected: true})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	client := s.client
	if !client.IsLoggedIn() || !client.IsConnected() {
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Bot is not connected", Connected: false})
		return
	}

	targetJID := types.JID{User: phone, Server: types.DefaultUserServer}
	ctx := context.Background()
	client.SubscribePresence(ctx, targetJID)
	client.SendChatPresence(ctx, targetJID, types.ChatPresenceComposing, types.ChatPresenceMediaText)
	time.Sleep(time.Duration(2000+rand.Intn(2000)) * time.Millisecond)
	client.SendChatPresence(ctx, targetJID, types.ChatPresencePaused, types.ChatPresenceMediaText)

	if _, err := client.SendMessage(ctx, targetJID, &waProto.Message{Conversation: proto.String(text)}); err != nil {
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: err.Error(), Connected: client.IsConnected()})
		return
	}

	time.Sleep(2 * time.Second)
	if err := client.SendAppState(ctx, appstate.BuildDeleteChat(targetJID, time.Now(), nil, true)); err != nil {
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: err.Error(), Connected: client.IsConnected()})
		return
	}
	_ = json.NewEncoder(w).Encode(APIResponse{Status: "success", Message: "Sent and chat deleted!", Connected: client.IsConnected()})
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	uid, ok := requireUserID(w, r)
	if !ok {
		return
	}

	s := getSession(uid)
	if s == nil || s.client == nil {
		_ = deleteUserSession(uid)
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "success", Message: "Already logged out"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	client := s.client
	ctx := context.Background()
	if client.IsLoggedIn() {
		if err := client.Logout(ctx); err != nil {
			_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: err.Error(), Connected: client.IsConnected()})
			return
		}
	}
	if client.Store != nil && client.Store.ID != nil {
		_ = waContainer.DeleteDevice(ctx, client.Store)
	}
	_ = deleteUserSession(uid)
	removeSession(uid)
	_ = json.NewEncoder(w).Encode(APIResponse{Status: "success", Message: "Logged out and session cleared", Connected: false})
}
