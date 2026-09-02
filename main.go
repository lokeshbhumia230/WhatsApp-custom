package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
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

var (
	client    *whatsmeow.Client
	container *sqlstore.Container
	sessionMu sync.RWMutex
)

type APIResponse struct {
	Status    string `json:"status"`
	Message   string `json:"message,omitempty"`
	Code      string `json:"code,omitempty"`
	Connected bool   `json:"connected"`
}

type StatusResponse struct {
	LoggedIn   bool   `json:"logged_in"`
	Connected  bool   `json:"connected"`
	State      string `json:"state"`
	ServerTime string `json:"server_time"`
}

func enableCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func getClient() *whatsmeow.Client {
	sessionMu.RLock()
	defer sessionMu.RUnlock()
	return client
}

func main() {
	dbLog := waLog.Stdout("Database", "WARN", true)
	var err error
	container, err = sqlstore.New(context.Background(), "sqlite3", "file:store.db?_foreign_keys=on", dbLog)
	if err != nil {
		panic(err)
	}

	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		panic(err)
	}

	clientLog := waLog.Stdout("Client", "INFO", true)
	newClient := whatsmeow.NewClient(deviceStore, clientLog)
	if err = newClient.Connect(); err != nil {
		panic(err)
	}

	sessionMu.Lock()
	client = newClient
	sessionMu.Unlock()

	http.HandleFunc("/", rootHandler)
	http.HandleFunc("/pair", pairHandler)
	http.HandleFunc("/send", sendHandler)
	http.HandleFunc("/status", statusHandler)
	http.HandleFunc("/logout", logoutHandler)

	fmt.Println("API Server running on port 3000...")
	if err = http.ListenAndServe(":3000", nil); err != nil {
		panic(err)
	}
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodOptions)
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(StatusResponse{State: "method_not_allowed"})
		return
	}

	current := getClient()
	response := StatusResponse{
		State:      "uninitialized",
		ServerTime: time.Now().UTC().Format(time.RFC3339Nano),
	}

	if current != nil {
		response.LoggedIn = current.IsLoggedIn()
		response.Connected = current.IsConnected()
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
	}

	statusCode := http.StatusServiceUnavailable
	if response.Connected {
		statusCode = http.StatusOK
	}
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(response)
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")

	current := getClient()
	connected := current != nil && current.IsConnected()
	loggedIn := current != nil && current.IsLoggedIn()
	status := "Not Logged In"
	if loggedIn && connected {
		status = "Logged In & Ready"
	} else if loggedIn {
		status = "Logged In but Disconnected"
	}

	_ = json.NewEncoder(w).Encode(APIResponse{
		Status: "success", Message: "API is running. State: " + status, Connected: connected,
	})
}

func pairHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodOptions)
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Method must be GET"})
		return
	}

	phone := r.URL.Query().Get("phone")
	if phone == "" {
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Invalid phone"})
		return
	}

	current := getClient()
	if current == nil {
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Client unavailable"})
		return
	}
	if current.IsLoggedIn() {
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Already paired! Use /logout to reset.", Connected: current.IsConnected()})
		return
	}

	code, err := current.PairPhone(context.Background(), phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: err.Error(), Connected: current.IsConnected()})
		return
	}

	_ = json.NewEncoder(w).Encode(APIResponse{Status: "success", Code: code, Connected: current.IsConnected()})
}

func sendHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodOptions)
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Method must be GET"})
		return
	}

	current := getClient()
	if current == nil || !current.IsLoggedIn() || !current.IsConnected() {
		connected := current != nil && current.IsConnected()
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Bot is not connected", Connected: connected})
		return
	}

	phone := r.URL.Query().Get("phone")
	text := r.URL.Query().Get("text")
	if phone == "" || text == "" {
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Phone and text are required", Connected: true})
		return
	}

	targetJID := types.JID{User: phone, Server: types.DefaultUserServer}
	ctx := context.Background()

	current.SubscribePresence(ctx, targetJID)
	current.SendChatPresence(ctx, targetJID, types.ChatPresenceComposing, types.ChatPresenceMediaText)
	time.Sleep(time.Duration(2000+rand.Intn(2000)) * time.Millisecond)
	current.SendChatPresence(ctx, targetJID, types.ChatPresencePaused, types.ChatPresenceMediaText)

	_, err := current.SendMessage(ctx, targetJID, &waProto.Message{Conversation: proto.String(text)})
	if err != nil {
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: err.Error(), Connected: current.IsConnected()})
		return
	}

	time.Sleep(2 * time.Second)
	patch := appstate.BuildDeleteChat(targetJID, time.Now(), nil, true)
	if err := current.SendAppState(context.Background(), patch); err != nil {
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: err.Error(), Connected: current.IsConnected()})
		return
	}

	_ = json.NewEncoder(w).Encode(APIResponse{Status: "success", Message: "Sent and chat deleted!", Connected: current.IsConnected()})
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost+", "+http.MethodOptions)
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Method must be POST"})
		return
	}

	current := getClient()
	if current == nil {
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "success", Message: "Already logged out", Connected: false})
		return
	}
	if !current.IsLoggedIn() {
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "success", Message: "Already logged out", Connected: current.IsConnected()})
		return
	}

	ctx := context.Background()
	if err := current.Logout(ctx); err != nil {
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: err.Error(), Connected: current.IsConnected()})
		return
	}

	devices, err := container.GetAllDevices(ctx)
	if err == nil {
		for _, device := range devices {
			_ = container.DeleteDevice(ctx, device)
		}
	}

	newClient := whatsmeow.NewClient(container.NewDevice(), waLog.Stdout("Client", "INFO", true))
	if err := newClient.Connect(); err != nil {
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: fmt.Sprintf("logged out, but failed to prepare a new session: %v", err), Connected: false})
		return
	}

	sessionMu.Lock()
	client = newClient
	sessionMu.Unlock()

	_ = json.NewEncoder(w).Encode(APIResponse{Status: "success", Message: "Logged out and local session data cleared", Connected: newClient.IsConnected()})
}
