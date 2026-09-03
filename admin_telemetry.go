package main

import (
    "encoding/json"
    "net/http"
    "strings"
)

type telemetryRow struct {
    Country string `json:"country"`
    Attempts int `json:"attempts"`
    LIDResolved int `json:"lid_resolved"`
    LIDNotResolved int `json:"lid_not_resolved"`
    LIDLookupFailed int `json:"lid_lookup_failed"`
    LIDLookupRateLimited int `json:"lid_lookup_rate_limited"`
    SendSuccess int `json:"send_success"`
    SendNoLID int `json:"send_no_lid"`
    SendRateLimited int `json:"send_rate_limited"`
    SendTimeout int `json:"send_timeout"`
    SendFailed int `json:"send_failed"`
    SafetyBlocked int `json:"safety_blocked"`
    PrecheckFailed int `json:"precheck_failed"`
}

func adminTelemetryDataHandler(w http.ResponseWriter, r *http.Request) {
    if !requireAdmin(w, r) { return }
    if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }
    if err := ensureSendTelemetryTable(); err != nil {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusInternalServerError)
        _ = json.NewEncoder(w).Encode(map[string]any{"status":"error", "message":err.Error()})
        return
    }

    rows, err := userDB.Query(`
        SELECT country,
          count(*) FILTER (WHERE stage='attempt'),
          count(*) FILTER (WHERE stage='lid_resolved'),
          count(*) FILTER (WHERE stage='lid_not_resolved'),
          count(*) FILTER (WHERE stage='lid_lookup_failed'),
          count(*) FILTER (WHERE stage='lid_lookup_rate_limited'),
          count(*) FILTER (WHERE stage='send_success'),
          count(*) FILTER (WHERE stage='send_no_lid'),
          count(*) FILTER (WHERE stage='send_rate_limited'),
          count(*) FILTER (WHERE stage='send_timeout'),
          count(*) FILTER (WHERE stage='send_failed'),
          count(*) FILTER (WHERE stage='safety_blocked'),
          count(*) FILTER (WHERE stage='precheck_failed')
        FROM public.send_telemetry
        WHERE created_at >= now() - interval '7 days'
        GROUP BY country ORDER BY CASE country WHEN 'IN' THEN 1 WHEN 'BR' THEN 2 ELSE 3 END, country`)
    if err != nil { w.Header().Set("Content-Type", "application/json"); w.WriteHeader(http.StatusInternalServerError); _ = json.NewEncoder(w).Encode(map[string]any{"status":"error","message":err.Error()}); return }
    defer rows.Close()
    out := []telemetryRow{}
    for rows.Next() {
        var x telemetryRow
        if err := rows.Scan(&x.Country,&x.Attempts,&x.LIDResolved,&x.LIDNotResolved,&x.LIDLookupFailed,&x.LIDLookupRateLimited,&x.SendSuccess,&x.SendNoLID,&x.SendRateLimited,&x.SendTimeout,&x.SendFailed,&x.SafetyBlocked,&x.PrecheckFailed); err != nil { continue }
        out = append(out, x)
    }
    _ = rows.Close()
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]any{"status":"success","window_days":7,"countries":out})
}

func adminTelemetryPageHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }
    data := `<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"><title>88Task • Send Telemetry</title><style>body{font:14px system-ui;margin:0;color:#111;background:#fff}.wrap{max-width:1200px;margin:auto;padding:24px}.top{display:flex;justify-content:space-between;align-items:center;gap:12px}.muted{color:#777}.btn{padding:9px 12px;border:1px solid #ddd;border-radius:8px;background:#fff;font-weight:700;text-decoration:none;color:#111}.card{border:1px solid #e5e5e5;border-radius:14px;padding:18px;margin-top:16px;overflow:auto}table{width:100%;border-collapse:collapse;min-width:900px}th,td{padding:10px 8px;border-bottom:1px solid #eee;text-align:right}th:first-child,td:first-child{text-align:left}th{font-size:12px;color:#666;white-space:nowrap}.big{font-size:28px;font-weight:800}.ok{font-weight:800}@media(max-width:700px){.wrap{padding:16px}.card{padding:12px}}</style></head><body><main class="wrap"><div class="top"><div><h1>Send Telemetry</h1><div class="muted">Last 7 days • India vs Brazil vs other destinations</div></div><a class="btn" href="/admin">Dashboard</a></div><div id="summary" class="card">Loading…</div><div class="card"><table><thead><tr><th>Country</th><th>Attempts</th><th>LID resolved</th><th>LID missing</th><th>LID lookup fail</th><th>LID 429</th><th>Sent</th><th>No LID send</th><th>Send 429</th><th>Timeout</th><th>Other fail</th><th>Safety blocked</th></tr></thead><tbody id="body"></tbody></table></div><div class="card"><div class="muted">Interpretation: a high <b>LID missing / lookup failure</b> rate points to recipient resolution; a high <b>send 429</b> rate points to rate limiting; a high <b>send success</b> rate means the pipeline itself is working for that country.</div></div></main><script>const key='admin_token';function pct(a,b){return b?((a/b)*100).toFixed(1)+'%':'—'}async function load(){let t=sessionStorage.getItem(key);if(!t){location.href='/admin';return}let r=await fetch('/admin/telemetry/data',{headers:{'X-Admin-Token':t}});if(r.status===401){sessionStorage.removeItem(key);location.href='/admin';return}let d=await r.json(),xs=d.countries||[];document.getElementById('summary').innerHTML=xs.length?xs.map(x=>'<div style="display:inline-block;min-width:180px;margin-right:28px"><div class="big">'+x.send_success+' / '+x.attempts+'</div><div class="muted">'+x.country+' sent ('+pct(x.send_success,x.attempts)+')</div></div>').join(''):'No send data yet.';document.getElementById('body').innerHTML=xs.map(x=>'<tr><td><b>'+x.country+'</b></td><td>'+x.attempts+'</td><td>'+x.lid_resolved+' ('+pct(x.lid_resolved,x.attempts)+')</td><td>'+x.lid_not_resolved+'</td><td>'+x.lid_lookup_failed+'</td><td>'+x.lid_lookup_rate_limited+'</td><td class="ok">'+x.send_success+' ('+pct(x.send_success,x.attempts)+')</td><td>'+x.send_no_lid+'</td><td>'+x.send_rate_limited+'</td><td>'+x.send_timeout+'</td><td>'+x.send_failed+'</td><td>'+x.safety_blocked+'</td></tr>').join('')}load()</script></body></html>`
    w.Header().Set("Cache-Control", "no-store")
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    _, _ = w.Write([]byte(data))
}

func init() {
    http.HandleFunc("/admin/telemetry", adminTelemetryPageHandler)
    http.HandleFunc("/admin/telemetry/data", adminTelemetryDataHandler)
    _ = strings.TrimSpace
}
