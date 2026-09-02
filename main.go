package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

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

const maxMessageRunes = 4096

type APIResponse struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	Code    string `json:"code,omitempty"`
}

// CORS Helper Function
func enableCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", http.MethodPost)
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

// writeJSON is the single response path for API handlers. Encoding can fail if a
// response type is changed in the future, so log it even though headers may
// already have been sent to the client.
func writeJSON(w http.ResponseWriter, status int, response APIResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("encode API response: %v", err)
	}
}

func requirePost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodPost {
		return true
	}
	w.Header().Set("Allow", http.MethodPost)
	writeJSON(w, http.StatusMethodNotAllowed, APIResponse{Status: "error", Message: "Method not allowed"})
	return false
}

// normalizePhone converts common presentation separators to the digit-only
// identifier required by WhatsApp. It accepts E.164-length identifiers only.
func normalizePhone(phone string) (string, bool) {
	phone = strings.TrimSpace(phone)
	if strings.HasPrefix(phone, "+") {
		phone = phone[1:]
	}

	var digits strings.Builder
	for _, char := range phone {
		switch {
		case char >= '0' && char <= '9':
			digits.WriteRune(char)
		case char == ' ' || char == '-' || char == '(' || char == ')' || char == '.':
			// Ignore common formatting characters.
		default:
			return "", false
		}
	}

	normalized := digits.String()
	return normalized, len(normalized) >= 7 && len(normalized) <= 15
}

func validMessage(text string) (string, bool) {
	text = strings.TrimSpace(text)
	return text, text != "" && utf8.RuneCountInString(text) <= maxMessageRunes
}

func main() {
	dbLog := waLog.Stdout("Database", "WARN", true)
	container, err := sqlstore.New(context.Background(), "sqlite3", "file:store.db?_foreign_keys=on", dbLog)
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

	fmt.Println("API Server running on port 3000...")
	err = http.ListenAndServe(":3000", nil)
	if err != nil {
		panic(err)
	}
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	status := "Not Logged In"
	if client != nil && client.IsLoggedIn() {
		status = "Logged In & Ready"
	}
	writeJSON(w, http.StatusOK, APIResponse{Status: "success", Message: "API is running. State: " + status})
}

func pairHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	if !requirePost(w, r) {
		return
	}
	phone, valid := normalizePhone(r.URL.Query().Get("phone"))
	if !valid {
		writeJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Invalid phone"})
		return
	}
	if client != nil && client.IsLoggedIn() {
		writeJSON(w, http.StatusConflict, APIResponse{Status: "error", Message: "Already paired! Delete store.db to reset."})
		return
	}
	if client == nil {
		writeJSON(w, http.StatusBadGateway, APIResponse{Status: "error", Message: "Pairing service is unavailable"})
		return
	}
	code, err := client.PairPhone(context.Background(), phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		log.Printf("pair phone: %v", err)
		writeJSON(w, http.StatusBadGateway, APIResponse{Status: "error", Message: "Unable to pair phone"})
		return
	}
	writeJSON(w, http.StatusOK, APIResponse{Status: "success", Code: code})
}

func sendHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	if !requirePost(w, r) {
		return
	}
	if client == nil || !client.IsLoggedIn() {
		writeJSON(w, http.StatusUnauthorized, APIResponse{Status: "error", Message: "Bot is not logged in"})
		return
	}
	phone, valid := normalizePhone(r.URL.Query().Get("phone"))
	if !valid {
		writeJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Invalid phone"})
		return
	}
	text, valid := validMessage(r.URL.Query().Get("text"))
	if !valid {
		writeJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: fmt.Sprintf("Message must be between 1 and %d characters", maxMessageRunes)})
		return
	}

	targetJID := types.JID{User: phone, Server: types.DefaultUserServer}
	ctx := context.Background()

	if err := client.SubscribePresence(ctx, targetJID); err != nil {
		log.Printf("subscribe presence: %v", err)
		writeJSON(w, http.StatusBadGateway, APIResponse{Status: "error", Message: "Unable to prepare message"})
		return
	}
	if err := client.SendChatPresence(ctx, targetJID, types.ChatPresenceComposing, types.ChatPresenceMediaText); err != nil {
		log.Printf("send composing presence: %v", err)
		writeJSON(w, http.StatusBadGateway, APIResponse{Status: "error", Message: "Unable to prepare message"})
		return
	}
	time.Sleep(time.Duration(2000+rand.Intn(2000)) * time.Millisecond)
	if err := client.SendChatPresence(ctx, targetJID, types.ChatPresencePaused, types.ChatPresenceMediaText); err != nil {
		log.Printf("send paused presence: %v", err)
		writeJSON(w, http.StatusBadGateway, APIResponse{Status: "error", Message: "Unable to prepare message"})
		return
	}

	_, err := client.SendMessage(ctx, targetJID, &waProto.Message{Conversation: proto.String(text)})
	if err != nil {
		log.Printf("send message: %v", err)
		writeJSON(w, http.StatusBadGateway, APIResponse{Status: "error", Message: "Unable to send message"})
		return
	}

	go func() {
		time.Sleep(2 * time.Second)
		patch := appstate.BuildDeleteChat(targetJID, time.Now(), nil, true)
		client.SendAppState(context.Background(), patch)
	}()

	writeJSON(w, http.StatusOK, APIResponse{Status: "success", Message: "Sent and chat deleted!"})
}
