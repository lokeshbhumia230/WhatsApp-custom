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

// Global client variable
var client *whatsmeow.Client

// API Response format
type APIResponse struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	Code    string `json:"code,omitempty"`
}

func main() {
	// 1. Database setup
	dbLog := waLog.Stdout("Database", "WARN", true)
	container, err := sqlstore.New(context.Background(), "sqlite3", "file:store.db?_foreign_keys=on", dbLog)
	if err != nil {
		panic(err)
	}

	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		panic(err)
	}

	// 2. Client setup
	clientLog := waLog.Stdout("Client", "INFO", true)
	client = whatsmeow.NewClient(deviceStore, clientLog)

	err = client.Connect()
	if err != nil {
		panic(err)
	}

	// 3. API Endpoints setup
	http.HandleFunc("/", rootHandler)
	http.HandleFunc("/pair", pairHandler)
	http.HandleFunc("/send", sendHandler)

	// 4. Server Start
	fmt.Println("======================================")
	fmt.Println(" API Server Running on port 3000")
	fmt.Println(" 1. Status: http://localhost:3000/")
	fmt.Println(" 2. Pair:   http://localhost:3000/pair?phone=919999999999")
	fmt.Println(" 3. Send:   http://localhost:3000/send?phone=919999999999&text=Hello")
	fmt.Println("======================================")
	
	err = http.ListenAndServe(":3000", nil)
	if err != nil {
		panic(err)
	}
}

// Endpoint 1: Root ("/") - Server Status Check
func rootHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	
	status := "Not Logged In"
	if client.IsLoggedIn() {
		status = "Logged In & Ready"
	}

	json.NewEncoder(w).Encode(APIResponse{
		Status:  "success",
		Message: "Bot is running. Current state: " + status,
	})
}

// Endpoint 2: Pair ("/pair") - Generate Code
func pairHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	phone := r.URL.Query().Get("phone")
	if phone == "" || len(phone) < 10 || len(phone) > 15 {
		json.NewEncoder(w).Encode(APIResponse{
			Status:  "error",
			Message: "Invalid phone number length. It should be between 10 to 15 digits (with country code).",
		})
		return
	}

	if client.IsLoggedIn() {
		json.NewEncoder(w).Encode(APIResponse{
			Status:  "error",
			Message: "Bot is already logged in! Please delete store.db to pair a new device.",
		})
		return
	}

	code, err := client.PairPhone(context.Background(), phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		json.NewEncoder(w).Encode(APIResponse{
			Status:  "error",
			Message: err.Error(),
		})
		return
	}

	json.NewEncoder(w).Encode(APIResponse{
		Status: "success",
		Code:   code,
	})
}

// Endpoint 3: Send ("/send") - Message with Anti-Ban & Auto-Delete Entire Chat
func sendHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if !client.IsLoggedIn() {
		json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Bot is not logged in"})
		return
	}

	phone := r.URL.Query().Get("phone")
	text := r.URL.Query().Get("text")

	if phone == "" || text == "" {
		json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Phone and text parameters are required"})
		return
	}

	// JID (WhatsApp ID) create karna
	targetJID := types.JID{
		User:   phone,
		Server: types.DefaultUserServer, // s.whatsapp.net
	}

	// Background context create karna
	ctx := context.Background()

	// --- ANTI-BAN SIMULATION START ---
	// 1. WhatsApp ko notify karein ki hum online hain
	client.SubscribePresence(ctx, targetJID)
	
	// 2. "Typing..." state send karein
	client.SendChatPresence(ctx, targetJID, types.ChatPresenceComposing, types.ChatPresenceMediaText)
	
	// 3. Random delay (2 se 4 seconds) taaki bot human lage
	sleepDuration := time.Duration(2000+rand.Intn(2000)) * time.Millisecond
	time.Sleep(sleepDuration)
	
	// 4. "Typing..." rok dein (Paused)
	client.SendChatPresence(ctx, targetJID, types.ChatPresencePaused, types.ChatPresenceMediaText)
	// --- ANTI-BAN SIMULATION END ---

	// Message Bhejna
	_, err := client.SendMessage(ctx, targetJID, &waProto.Message{
		Conversation: proto.String(text),
	})

	if err != nil {
		json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Failed to send: " + err.Error()})
		return
	}

	// --- DELETE ENTIRE CHAT LOGIC START ---
	go func() {
		// Thoda wait karke delete karo taaki message server tak theek se chala jaye
		time.Sleep(2 * time.Second)
		
		// BuildDeleteChat directly ek complete PatchInfo return karta hai
		patch := appstate.BuildDeleteChat(targetJID, time.Now(), nil, true)
		
		// Isey seedha pass karna hai, bina [] ke
		client.SendAppState(context.Background(), patch)
	}()
	// --- DELETE LOGIC END ---

	json.NewEncoder(w).Encode(APIResponse{
		Status:  "success",
		Message: "Message sent and the entire chat will be cleared from your side in 2 seconds!",
	})
}
