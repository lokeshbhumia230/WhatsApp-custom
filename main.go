package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
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

var client *whatsmeow.Client

type APIResponse struct {
	Status    string `json:"status"`
	Message   string `json:"message,omitempty"`
	Code      string `json:"code,omitempty"`
	Connected bool   `json:"connected"`
}

// CORS Helper Function
func enableCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func main() {
	dbLog := waLog.Stdout("Database", "WARN", true)
	container, err := sqlstore.New(context.Background(), "sqlite3", "file:store.db?_foreign_keys=on", dbLog)
	if err != nil { panic(err) }

	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil { panic(err) }

	clientLog := waLog.Stdout("Client", "INFO", true)
	client = whatsmeow.NewClient(deviceStore, clientLog)

	err = client.Connect()
	if err != nil { panic(err) }

	http.HandleFunc("/", rootHandler)
	http.HandleFunc("/status", statusHandler)
	http.HandleFunc("/pair", pairHandler)
	http.HandleFunc("/send", sendHandler)

	fmt.Println("API Server running on port 3000...")
	err = http.ListenAndServe(":3000", nil)
	if err != nil { panic(err) }
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	connected := client.IsLoggedIn()
	status := "Not Logged In"
	if connected { status = "Logged In & Ready" }
	json.NewEncoder(w).Encode(APIResponse{Status: "success", Message: "API is running. State: " + status, Connected: connected})
}

// statusHandler provides a lightweight endpoint that the frontend can poll
// after pairing so the UI updates automatically without a manual refresh.
func statusHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	connected := client.IsLoggedIn()
	status := "Not Logged In"
	if connected { status = "Logged In & Ready" }
	json.NewEncoder(w).Encode(APIResponse{Status: "success", Message: status, Connected: connected})
}

func pairHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	phone := r.URL.Query().Get("phone")
	if phone == "" {
		json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Invalid phone"})
		return
	}
	if client.IsLoggedIn() {
		json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Already paired! Delete store.db to reset."})
		return
	}
	code, err := client.PairPhone(context.Background(), phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: err.Error()})
		return
	}
	json.NewEncoder(w).Encode(APIResponse{Status: "success", Code: code, Connected: client.IsLoggedIn()})
}

func sendHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	if !client.IsLoggedIn() {
		json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Bot is not logged in", Connected: false})
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
		json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: err.Error(), Connected: client.IsLoggedIn()})
		return
	}

	go func() {
		time.Sleep(2 * time.Second)
		patch := appstate.BuildDeleteChat(targetJID, time.Now(), nil, true)
		client.SendAppState(context.Background(), patch)
	}()

	json.NewEncoder(w).Encode(APIResponse{Status: "success", Message: "Sent and chat deleted!", Connected: true})
}
