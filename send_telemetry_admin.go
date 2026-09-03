package main

import (
    "encoding/json"
    "net/http"
)

func sendTelemetryAdminHandler(w http.ResponseWriter, r *http.Request) {
    enableCORS(w)
    if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }
    if !checkAdminToken(r) { w.Header().Set("Content-Type", "application/json"); w.WriteHeader(http.StatusUnauthorized); _ = json.NewEncoder(w).Encode(APIResponse{Status:"error",Message:"Unauthorized"}); return }
    if err := ensureSendTelemetryTable(); err != nil { bulkJSON(w, 500, APIResponse{Status:"error",Message:"Telemetry table initialization failed: "+err.Error()}); return }

    rows, err := userDB.Query(`SELECT country,stage,count(*) FROM public.send_telemetry WHERE created_at >= now()-interval '7 days' GROUP BY country,stage ORDER BY country,stage`)
    if err != nil { bulkJSON(w, 500, APIResponse{Status:"error",Message:err.Error()}); return }
    defer rows.Close()
    byCountry := map[string]map[string]int{}
    for rows.Next() {
        var country, stage string; var n int
        if err := rows.Scan(&country,&stage,&n); err != nil { bulkJSON(w,500,APIResponse{Status:"error",Message:err.Error()}); return }
        if byCountry[country] == nil { byCountry[country] = map[string]int{} }
        byCountry[country][stage] = n
    }
    if err := rows.Err(); err != nil { bulkJSON(w,500,APIResponse{Status:"error",Message:err.Error()}); return }

    out := map[string]any{"status":"success","window":"7d","by_country":byCountry,"stages":[]string{"attempt","safety_blocked","lid_resolved","lid_not_resolved","lid_lookup_failed","lid_lookup_rate_limited","send_success","send_no_lid","send_rate_limited","send_timeout","send_failed"}}
    bulkJSON(w,200,out)
}

func init() { http.HandleFunc("/admin/telemetry", sendTelemetryAdminHandler) }
