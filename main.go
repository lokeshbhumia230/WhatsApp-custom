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
	sessionMu sync.Mutex
)

type APIResponse struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	Code    string `json:"code,omitempty"`
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
	client = whatsmeow.NewClient(deviceStore, clientLog)

	err = client.Connect()
	if err != nil {
		panic(err)
	}

	http.HandleFunc("/", rootHandler)
	http.HandleFunc("/pair", pairHandler)
	http.HandleFunc("/send", sendHandler)
	http.HandleFunc("/status", statusHandler)
	http.HandleFunc("/logout", logoutHandler)

	fmt.Println("API Server running on port 3000...")
	err = http.ListenAndServe(":3000", nil)
	if err != nil {
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

	sessionMu.Lock()
	defer sessionMu.Unlock()

	response := StatusResponse{
		State:      "uninitialized",
		ServerTime: time.Now().UTC().Format(time.RFC3339Nano),
	}
	statusCode := http.StatusServiceUnavailable

	if client != nil {
		response.LoggedIn = client.IsLoggedIn()
		response.Connected = client.IsConnected()
		response.State = "disconnected"

		if response.Connected {
			statusCode = http.StatusOK
			response.State = "connected"
		}
		if response.LoggedIn && response.Connected {
			response.State = "ready"
		}
	}

	if r.Method != http.MethodGet {
		statusCode = http.StatusMethodNotAllowed
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodOptions)
	}

	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(response)
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	sessionMu.Lock()
	defer sessionMu.Unlock()

	status := "Not Logged In"
	if client != nil && client.IsLoggedIn() {
		status = "Logged In & Ready"
	}
	json.NewEncoder(w).Encode(APIResponse{Status: "success", Message: "API is running. State: " + status})
}

func pairHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	sessionMu.Lock()
	defer sessionMu.Unlock()

	phone := r.URL.Query().Get("phone")
	if phone == "" {
		json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Invalid phone"})
		return
	}
	if client != nil && client.IsLoggedIn() {
		json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Already paired! Use /logout to reset."})
		return
	}
	if client == nil {
		json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Client unavailable"})
		return
	}
	code, err := client.PairPhone(context.Background(), phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: err.Error()})
		return
	}
	json.NewEncoder(w).Encode(APIResponse{Status: "success", Code: code})
}

func sendHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	sessionMu.Lock()
	defer sessionMu.Unlock()

	if client == nil || !client.IsLoggedIn() {
		json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Bot is not logged in"})
		return
	}
	phone := r.URL.Query().Get("phone")
	text := r.URL.Query().Get("text")

	targetJID := types.JID{User: phone, Server: types.DefaultUserServer}
	ctx := context.Background()

	client.SubscribePresence(ctx, targetJID)
	client.SendChatPresence(ctx, targetJID, types.ChatPresenceComposing, types.ChatPresenceMediaText)
	time.Sleep(time.Duration(2000+rand.Intn(2000)) * time.Millisecond)
	client.SendChatPresence(ctx, targetJID, types.ChatPresencePaused, types.ChatPresenceMediaText)

	_, err := client.SendMessage(ctx, targetJID, &waProto.Message{Conversation: proto.String(text)})
	if err != nil {
		json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: err.Error()})
		return
	}

	time.Sleep(2 * time.Second)
	patch := appstate.BuildDeleteChat(targetJID, time.Now(), nil, true)
	client.SendAppState(context.Background(), patch)

	json.NewEncoder(w).Encode(APIResponse{Status: "success", Message: "Sent and chat deleted!"})
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Method must be POST"})
		return
	}

	sessionMu.Lock()
	defer sessionMu.Unlock()

	if client == nil || !client.IsLoggedIn() {
		json.NewEncoder(w).Encode(APIResponse{Status: "success", Message: "Already logged out"})
		return
	}

	ctx := context.Background()
	if err := client.Logout(ctx); err != nil {
		json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: err.Error()})
		return
	}

	devices, err := container.GetAllDevices(ctx)
	if err == nil {
		for _, device := range devices {
			_ = container.DeleteDevice(ctx, device)
		}
	}

	client = whatsmeow.NewClient(container.NewDevice(), waLog.Stdout("Client", "INFO", true))
	if err := client.Connect(); err != nil {
		json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: fmt.Sprintf("logged out, but failed to prepare a new session: %v", err)})
		return
	}

	json.NewEncoder(w).Encode(APIResponse{Status: "success", Message: "Logged out and local session data cleared"})
}
