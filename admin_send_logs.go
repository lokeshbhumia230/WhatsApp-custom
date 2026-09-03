package main

import (
    "encoding/json"
    "net/http"
)

func adminSendLogsDataHandler(w http.ResponseWriter, r *http.Request) {
    if !requireAdmin(w, r) { return }
    if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }
    if err := ensureBulkMessageTable(); err != nil { http.Error(w, err.Error(), 500); return }
    rows, err := userDB.Query(`SELECT id,COALESCE(phone,target,''),COALESCE(message,''),COALESCE(consent,''),status,attempts,COALESCE(last_error,''),COALESCE(assigned_sender,''),created_at,updated_at,sent_at,COALESCE(campaign_id,'') FROM public.bulk_messages ORDER BY id DESC LIMIT 500`)
    if err != nil { http.Error(w, err.Error(), 500); return }
    defer rows.Close()
    out := make([]map[string]any,0)
    for rows.Next() {
        var id, attempts int64; var phone,msg,consent,status,lastErr,sender,campaign string; var created,updated,sent any
        if err:=rows.Scan(&id,&phone,&msg,&consent,&status,&attempts,&lastErr,&sender,&created,&updated,&sent,&campaign); err!=nil { continue }
        out=append(out,map[string]any{"id":id,"phone":phone,"message":msg,"consent":consent,"status":status,"attempts":attempts,"last_error":lastErr,"assigned_sender":sender,"created_at":created,"updated_at":updated,"sent_at":sent,"campaign_id":campaign})
    }
    w.Header().Set("Content-Type","application/json"); json.NewEncoder(w).Encode(map[string]any{"status":"success","rows":out})
}

func adminSendLogsPageHandler(w http.ResponseWriter,r *http.Request) {
    if r.Method!=http.MethodGet { w.WriteHeader(405); return }
    w.Header().Set("Content-Type","text/html; charset=utf-8"); w.Header().Set("Cache-Control","no-store")
    _,_=w.Write([]byte(`<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"><title>88Task • Send Logs</title><style>body{font:14px system-ui;margin:0;color:#111}.wrap{padding:20px;max-width:1500px;margin:auto}.top{display:flex;justify-content:space-between;align-items:center}.btn{padding:9px 12px;border:1px solid #ddd;border-radius:8px;text-decoration:none;color:#111;font-weight:700}.card{border:1px solid #ddd;border-radius:12px;padding:14px;margin-top:15px;overflow:auto}.filters{display:flex;gap:8px;flex-wrap:wrap}.filters input,.filters select{padding:9px;border:1px solid #ddd;border-radius:8px}.count{color:#777;margin-top:8px}table{border-collapse:collapse;width:100%;min-width:1250px}th,td{padding:9px;border-bottom:1px solid #eee;text-align:left;vertical-align:top}th{font-size:12px;color:#666;white-space:nowrap}.sent{font-weight:800}.failed{font-weight:800}.msg{max-width:300px;white-space:pre-wrap;word-break:break-word}.err{max-width:360px;white-space:pre-wrap;word-break:break-word}.muted{color:#777}</style></head><body><main class="wrap"><div class="top"><div><h1>Send Logs</h1><div class="muted">Last 500 rows from public.bulk_messages</div></div><a class="btn" href="/admin">Dashboard</a></div><div class="card"><div class="filters"><input id="q" placeholder="Search number / error / sender"><select id="s"><option value="">All statuses</option><option>queued</option><option>sending</option><option>sent</option><option>failed</option><option>paused</option><option>cancelled</option></select><button class="btn" onclick="load()">Refresh</button></div><div id="count" class="count"></div></div><div class="card"><table><thead><tr><th>ID</th><th>Phone</th><th>Status</th><th>Attempts</th><th>Why failed / Last error</th><th>Sender</th><th>Message</th><th>Created</th><th>Updated</th><th>Sent</th><th>Campaign</th></tr></thead><tbody id="body"></tbody></table></div></main><script>const key='admin_token';let rows=[];function esc(v){return String(v??'').replace(/[&<>\"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','\"':'&quot;',"'":'&#39;'}[c]))}function render(){let q=document.getElementById('q').value.toLowerCase(),s=document.getElementById('s').value;let x=rows.filter(r=>(!s||r.status===s)&&(!q||[r.phone,r.last_error,r.assigned_sender,r.message].join(' ').toLowerCase().includes(q)));document.getElementById('count').textContent=x.length+' rows';document.getElementById('body').innerHTML=x.map(r=>'<tr><td>'+r.id+'</td><td><b>'+esc(r.phone)+'</b></td><td class="'+(r.status==='sent'?'sent':r.status==='failed'?'failed':'')+'">'+esc(r.status)+'</td><td>'+r.attempts+'</td><td class="err">'+esc(r.last_error||'—')+'</td><td>'+esc(r.assigned_sender||'—')+'</td><td class="msg">'+esc(r.message)+'</td><td>'+esc(r.created_at)+'</td><td>'+esc(r.updated_at)+'</td><td>'+esc(r.sent_at||'—')+'</td><td>'+esc(r.campaign_id||'—')+'</td></tr>').join('')}async function load(){let t=sessionStorage.getItem(key);if(!t){location.href='/admin';return}let r=await fetch('/admin/send-logs/data',{headers:{'X-Admin-Token':t}});if(r.status===401){sessionStorage.removeItem(key);location.href='/admin';return}let d=await r.json();rows=d.rows||[];render()}document.getElementById('q').oninput=render;document.getElementById('s').onchange=render;load();setInterval(load,10000)</script></body></html>`))
}
func init(){ http.HandleFunc("/admin/send-logs",adminSendLogsPageHandler); http.HandleFunc("/admin/send-logs/data",adminSendLogsDataHandler) }
