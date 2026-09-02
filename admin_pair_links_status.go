package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

func adminPairLinksStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := ensurePairLinksTable(); err != nil {
		http.Error(w, "Could not initialize pairing links", http.StatusInternalServerError)
		return
	}

	uid := strings.TrimSpace(r.URL.Query().Get("user_id"))
	query := `SELECT token,user_id,created_at,expires_at,used FROM pairing_links`
	args := []any{}
	if uid != "" {
		query += ` WHERE user_id=$1`
		args = append(args, uid)
	}
	query += ` ORDER BY created_at DESC LIMIT 50`

	rows, err := userDB.Query(query, args...)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(APIResponse{Status: "error", Message: "Could not load pairing link status"})
		return
	}
	defer rows.Close()

	base := strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/")
	if base == "" {
		base = "https://whatsapp-custom-1.onrender.com"
	}
	now := time.Now().UTC()
	links := make([]map[string]any, 0)
	for rows.Next() {
		var token, userID string
		var createdAt, expiresAt time.Time
		var used bool
		if err := rows.Scan(&token, &userID, &createdAt, &expiresAt, &used); err != nil {
			continue
		}
		status := "pending"
		if used {
			status = "linked"
		} else if now.After(expiresAt) {
			status = "expired"
		}
		links = append(links, map[string]any{
			"user_id": userID,
			"link": base + "/connect/" + token,
			"created_at": createdAt.UTC().Format(time.RFC3339),
			"expires_at": expiresAt.UTC().Format(time.RFC3339),
			"status": status,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "links": links})
}
