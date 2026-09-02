package main

import (
 "context"
 "encoding/json"
 "net/http"
)

// logoutHandler is the admin WhatsApp-device logout endpoint.
// The user-portal /logout endpoint remains separate and only clears the portal session.
func logoutHandler(w http.ResponseWriter, r *http.Request) {
 enableCORS(w)
 w.Header().Set("Content-Type", "application/json")
 if r.Method != http.MethodPost && r.Method != http.MethodGet {
  w.WriteHeader(http.StatusMethodNotAllowed)
  return
 }
 uid := getUserID(r)
 if uid == "" {
  w.WriteHeader(http.StatusBadRequest)
  _ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "user_id is required"})
  return
 }
 s := removeSession(uid)
 if s == nil || s.client == nil {
  _ = deleteUserSession(uid)
  _ = json.NewEncoder(w).Encode(APIResponse{Status: "success", Message: "WhatsApp session removed", Connected: false})
  return
 }
 s.mu.Lock()
 if s.client.IsLoggedIn() {
  _ = s.client.Logout(context.Background())
 }
 s.mu.Unlock()
 _ = deleteUserSession(uid)
 _ = json.NewEncoder(w).Encode(APIResponse{Status: "success", Message: "WhatsApp logged out", Connected: false})
}
