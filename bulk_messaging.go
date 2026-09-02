package main

import (
    "crypto/sha256"
    "encoding/csv"
    "encoding/hex"
    "encoding/json"
    "io"
    "net/http"
    "sort"
    "strings"
    "time"

    "go.mau.fi/whatsmeow/types"
)

// ensureBulkMessageTable keeps the bulk queue compatible with older deployments.
// The queue has existed in a few schema variants, so every column used by the
// current worker/importer is explicitly ensured before an import is attempted.
func ensureBulkMessageTable() error {
    stmts := []string{
        `CREATE TABLE IF NOT EXISTS public.bulk_messages (
            id BIGSERIAL PRIMARY KEY,
            user_id TEXT,
            phone TEXT,
            target TEXT,
            message TEXT,
            consent TEXT,
            status TEXT NOT NULL DEFAULT 'queued',
            attempts INTEGER NOT NULL DEFAULT 0,
            last_error TEXT,
            assigned_sender TEXT,
            created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
            sent_at TIMESTAMPTZ,
            campaign_id TEXT,
            dedupe_key TEXT
        )`,
        `ALTER TABLE public.bulk_messages ADD COLUMN IF NOT EXISTS user_id TEXT`,
        `ALTER TABLE public.bulk_messages ADD COLUMN IF NOT EXISTS phone TEXT`,
        `ALTER TABLE public.bulk_messages ADD COLUMN IF NOT EXISTS target TEXT`,
        `ALTER TABLE public.bulk_messages ADD COLUMN IF NOT EXISTS message TEXT`,
        `ALTER TABLE public.bulk_messages ADD COLUMN IF NOT EXISTS consent TEXT`,
        `ALTER TABLE public.bulk_messages ADD COLUMN IF NOT EXISTS status TEXT DEFAULT 'queued'`,
        `ALTER TABLE public.bulk_messages ADD COLUMN IF NOT EXISTS attempts INTEGER DEFAULT 0`,
        `ALTER TABLE public.bulk_messages ADD COLUMN IF NOT EXISTS last_error TEXT`,
        `ALTER TABLE public.bulk_messages ADD COLUMN IF NOT EXISTS assigned_sender TEXT`,
        `ALTER TABLE public.bulk_messages ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ DEFAULT now()`,
        `ALTER TABLE public.bulk_messages ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ DEFAULT now()`,
        `ALTER TABLE public.bulk_messages ADD COLUMN IF NOT EXISTS sent_at TIMESTAMPTZ`,
        `ALTER TABLE public.bulk_messages ADD COLUMN IF NOT EXISTS campaign_id TEXT`,
        `ALTER TABLE public.bulk_messages ADD COLUMN IF NOT EXISTS dedupe_key TEXT`,
        `ALTER TABLE public.bulk_messages ALTER COLUMN user_id DROP NOT NULL`,
        `UPDATE public.bulk_messages SET target=phone WHERE (target IS NULL OR target='') AND phone IS NOT NULL AND phone<>''`,
        `UPDATE public.bulk_messages SET attempts=0 WHERE attempts IS NULL`,
        `UPDATE public.bulk_messages SET status='queued' WHERE status IS NULL OR status=''`,
        `UPDATE public.bulk_messages SET created_at=now() WHERE created_at IS NULL`,
        `UPDATE public.bulk_messages SET updated_at=now() WHERE updated_at IS NULL`,
        `DELETE FROM public.bulk_messages a USING public.bulk_messages b WHERE a.dedupe_key IS NOT NULL AND a.dedupe_key=b.dedupe_key AND a.ctid>b.ctid`,
        `CREATE UNIQUE INDEX IF NOT EXISTS bulk_messages_dedupe_uidx ON public.bulk_messages (dedupe_key) WHERE dedupe_key IS NOT NULL`,
        `CREATE INDEX IF NOT EXISTS bulk_messages_queue_idx ON public.bulk_messages (status, id)`,
    }
    for _, stmt := range stmts {
        if _, err := userDB.Exec(stmt); err != nil {
            return err
        }
    }
    return nil
}

func bulkConnectedDevices() []string {
    manager.mu.RLock()
    defer manager.mu.RUnlock()
    ids := []string{}
    for id, s := range manager.sessions {
        if s != nil && s.client != nil && s.client.IsLoggedIn() && s.client.IsConnected() {
            ids = append(ids, id)
        }
    }
    sort.Strings(ids)
    return ids
}

func normalizeBulkPhone(v string) (string, bool) {
    v = strings.TrimSpace(strings.TrimPrefix(v, "'"))
    v = strings.TrimPrefix(v, "+")
    v = strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(v)
    if len(v) < 7 || len(v) > 15 {
        return "", false
    }
    for _, c := range v {
        if c < '0' || c > '9' {
            return "", false
        }
    }
    return v, true
}

func bulkKey(c, p, m string) string {
    h := sha256.Sum256([]byte(c + "\x00" + p + "\x00" + m))
    return hex.EncodeToString(h[:])
}

func bulkQueuedCount() int {
    var n int
    _ = userDB.QueryRow(`SELECT count(*) FROM public.bulk_messages WHERE user_id IS NULL AND status='queued'`).Scan(&n)
    return n
}

func claimBulkRow(uid string) (int64, string, string, bool) {
    tx, err := userDB.Begin()
    if err != nil {
        return 0, "", "", false
    }
    defer tx.Rollback()

    var id int64
    var target, text string
    err = tx.QueryRow(`
        SELECT id,target,message
        FROM public.bulk_messages
        WHERE user_id IS NULL AND status='queued' AND target IS NOT NULL AND target<>''
        ORDER BY id
        FOR UPDATE SKIP LOCKED LIMIT 1`,
    ).Scan(&id, &target, &text)
    if err != nil {
        return 0, "", "", false
    }
    if _, err = tx.Exec(`
        UPDATE public.bulk_messages
        SET status='sending', attempts=attempts+1, assigned_sender=$1, updated_at=now()
        WHERE id=$2`, uid, id); err != nil {
        return 0, "", "", false
    }
    if err = tx.Commit(); err != nil {
        return 0, "", "", false
    }
    return id, target, text, true
}

func markBulkFailed(id int64, msg string) {
    _, _ = userDB.Exec(`UPDATE public.bulk_messages SET status='failed',last_error=$1,updated_at=now() WHERE id=$2`, msg, id)
}

func markBulkSent(id int64) {
    _, _ = userDB.Exec(`UPDATE public.bulk_messages SET status='sent',sent_at=now(),last_error=NULL,updated_at=now() WHERE id=$1`, id)
}

func bulkSenderForIndex(ids []string, i int) string {
    if len(ids) == 0 {
        return ""
    }
    return ids[i%len(ids)]
}

func processBulkMessagesGlobal() {
    if getAdminSetting("bulk_auto_send_enabled", "false") != "true" {
        return
    }
    ids := bulkConnectedDevices()
    if len(ids) == 0 {
        return
    }
    limit := safeSettingInt("bulk_batch_size", 5, 1, 20)
    for i := 0; i < limit; i++ {
        uid := bulkSenderForIndex(ids, i)
        id, target, text, ok := claimBulkRow(uid)
        if !ok {
            return
        }
        s := getSession(uid)
        if s == nil || s.client == nil {
            markBulkFailed(id, "WhatsApp account disconnected")
            continue
        }
        s.mu.Lock()
        err := safeSendMessage(uid, s.client, types.JID{User: target, Server: types.DefaultUserServer}, text)
        s.mu.Unlock()
        if err != nil {
            markBulkFailed(id, err.Error())
        } else {
            markBulkSent(id)
        }
    }
}

func bulkWorkerLoop() {
    for userDB == nil {
        time.Sleep(2 * time.Second)
    }
    for {
        processBulkMessagesGlobal()
        time.Sleep(10 * time.Second)
    }
}

func bulkJSON(w http.ResponseWriter, status int, v any) {
    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(v)
}

func bulkMessageStatusHandler(w http.ResponseWriter, r *http.Request) {
    enableCORS(w)
    if r.Method != http.MethodGet {
        bulkJSON(w, 405, APIResponse{Status: "error", Message: "Method not allowed"})
        return
    }
    out := map[string]any{"status": "success"}
    for _, st := range []string{"queued", "sending", "sent", "failed", "paused", "cancelled"} {
        var n int
        _ = userDB.QueryRow(`SELECT count(*) FROM public.bulk_messages WHERE user_id IS NULL AND status=$1`, st).Scan(&n)
        out[st] = n
    }
    devices := []DeviceInfo{}
    for _, uid := range bulkConnectedDevices() {
        s := getSession(uid)
        if s != nil && s.client != nil && s.client.Store != nil && s.client.Store.ID != nil {
            devices = append(devices, DeviceInfo{UserID: uid, Phone: s.client.Store.ID.User, Connected: true, LoggedIn: true, State: "ready"})
        }
    }
    out["connected_accounts"] = len(devices)
    out["devices"] = devices
    bulkJSON(w, 200, out)
}

func csvHeaderIndex(headers []string, names ...string) int {
    for i, h := range headers {
        h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))
        h = strings.NewReplacer("_", "", "-", "", " ", "").Replace(h)
        for _, name := range names {
            if h == strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(name)) {
                return i
            }
        }
    }
    return -1
}

func bulkMessageImportHandler(w http.ResponseWriter, r *http.Request) {
    enableCORS(w)
    if r.Method != http.MethodPost {
        bulkJSON(w, 405, APIResponse{Status: "error", Message: "Method not allowed"})
        return
    }
    if err := ensureBulkMessageTable(); err != nil {
        bulkJSON(w, 500, APIResponse{Status: "error", Message: "Bulk table initialization failed: " + err.Error()})
        return
    }

    if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
        var in struct {
            Action  string   `json:"action"`
            UserIDs []string `json:"user_ids"`
            Limit   int      `json:"limit"`
        }
        if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
            bulkJSON(w, 400, APIResponse{Status: "error", Message: "Invalid JSON: " + err.Error()})
            return
        }
        action := strings.ToLower(strings.TrimSpace(in.Action))
        if action != "send" {
            queries := map[string]string{
                "pause":        `UPDATE public.bulk_messages SET status='paused',updated_at=now() WHERE user_id IS NULL AND status='queued'`,
                "resume":       `UPDATE public.bulk_messages SET status='queued',updated_at=now() WHERE user_id IS NULL AND status='paused'`,
                "cancel":       `UPDATE public.bulk_messages SET status='cancelled',updated_at=now() WHERE user_id IS NULL AND status IN ('queued','paused','failed')`,
                "retry_failed": `UPDATE public.bulk_messages SET status='queued',attempts=0,last_error=NULL,assigned_sender=NULL,updated_at=now() WHERE user_id IS NULL AND status='failed'`,
                "clear_queue":  `DELETE FROM public.bulk_messages WHERE user_id IS NULL AND status IN ('queued','paused','cancelled','failed')`,
            }
            sql, ok := queries[action]
            if !ok {
                bulkJSON(w, 400, APIResponse{Status: "error", Message: "Unknown action"})
                return
            }
            res, err := userDB.Exec(sql)
            if err != nil {
                bulkJSON(w, 500, APIResponse{Status: "error", Message: err.Error()})
                return
            }
            n, _ := res.RowsAffected()
            bulkJSON(w, 200, map[string]any{"status": "success", "action": action, "affected": n})
            return
        }

        limit := in.Limit
        if limit < 1 {
            limit = 20
        }
        if limit > 200 {
            limit = 200
        }
        ids := in.UserIDs
        if len(ids) == 0 {
            ids = bulkConnectedDevices()
        }
        valid := map[string]bool{}
        for _, id := range bulkConnectedDevices() {
            valid[id] = true
        }
        selected := []string{}
        for _, id := range ids {
            if valid[id] {
                selected = append(selected, id)
            }
        }
        if len(selected) == 0 {
            bulkJSON(w, 400, APIResponse{Status: "error", Message: "No connected WhatsApp accounts selected"})
            return
        }

        sent, attempted := 0, 0
        for attempted < limit {
            uid := bulkSenderForIndex(selected, attempted)
            id, target, text, ok := claimBulkRow(uid)
            if !ok {
                break
            }
            attempted++
            s := getSession(uid)
            if s == nil || s.client == nil {
                markBulkFailed(id, "WhatsApp account disconnected")
                continue
            }
            s.mu.Lock()
            err := safeSendMessage(uid, s.client, types.JID{User: target, Server: types.DefaultUserServer}, text)
            s.mu.Unlock()
            if err != nil {
                markBulkFailed(id, err.Error())
            } else {
                markBulkSent(id)
                sent++
            }
        }
        bulkJSON(w, 200, map[string]any{"status": "success", "attempted": attempted, "sent": sent, "remaining_queue": bulkQueuedCount()})
        return
    }

    r.Body = http.MaxBytesReader(w, r.Body, 512<<20)
    mr, err := r.MultipartReader()
    if err != nil {
        bulkJSON(w, 400, APIResponse{Status: "error", Message: "Invalid multipart upload: " + err.Error()})
        return
    }
    var file io.Reader
    for {
        p, err := mr.NextPart()
        if err == io.EOF {
            break
        }
        if err != nil {
            bulkJSON(w, 400, APIResponse{Status: "error", Message: "Invalid multipart data: " + err.Error()})
            return
        }
        if p.FormName() == "file" {
            file = p
            break
        }
    }
    if file == nil {
        bulkJSON(w, 400, APIResponse{Status: "error", Message: "CSV file is required"})
        return
    }

    reader := csv.NewReader(file)
    reader.FieldsPerRecord = -1
    reader.TrimLeadingSpace = true
    campaign := time.Now().UTC().Format("20060102T150405.000000000Z")
    imported, skipped := 0, 0
    reasons := map[string]int{}
    var firstDatabaseError string
    first := true
    phoneIdx, msgIdx, consIdx := 0, 1, 2

    for {
        rec, err := reader.Read()
        if err == io.EOF {
            break
        }
        if err != nil {
            skipped++
            reasons["CSV parse error"]++
            continue
        }
        if first {
            first = false
            phoneIdx = csvHeaderIndex(rec, "phone", "phone number", "number", "mobile", "mobile number")
            msgIdx = csvHeaderIndex(rec, "message", "text", "body", "content")
            consIdx = csvHeaderIndex(rec, "consent", "optin", "opted in", "opt-in")
            if phoneIdx < 0 {
                phoneIdx = 0
            }
            if msgIdx < 0 {
                msgIdx = 1
            }
            if consIdx < 0 {
                consIdx = 2
            }
            joined := strings.ToLower(strings.Join(rec, ","))
            hasHeaderWord := strings.Contains(joined, "phone") && (strings.Contains(joined, "message") || strings.Contains(joined, "text") || strings.Contains(joined, "body"))
            if hasHeaderWord {
                continue
            }
        }

        if len(rec) <= phoneIdx || len(rec) <= msgIdx {
            skipped++
            reasons["missing phone/message"]++
            continue
        }
        phone, ok := normalizeBulkPhone(rec[phoneIdx])
        if !ok {
            skipped++
            reasons["invalid phone number"]++
            continue
        }
        msg := strings.TrimSpace(msgIdx < len(rec) ? rec[msgIdx] : "")
        if msg == "" {
            skipped++
            reasons["empty message"]++
            continue
        }
        if len(msg) > 10000 {
            skipped++
            reasons["message too long"]++
            continue
        }
        if consIdx < 0 || len(rec) <= consIdx {
            skipped++
            reasons["missing consent"]++
            continue
        }
        consent := strings.ToLower(strings.TrimSpace(rec[consIdx]))
        if consent != "yes" && consent != "true" && consent != "1" {
            skipped++
            reasons["consent not granted"]++
            continue
        }

        key := bulkKey(campaign, phone, msg)
        res, err := userDB.Exec(`
            INSERT INTO public.bulk_messages(user_id,target,phone,message,consent,status,attempts,campaign_id,dedupe_key,created_at,updated_at)
            VALUES(NULL,$1,$1,$2,$3,'queued',0,$4,$5,now(),now())
            ON CONFLICT(dedupe_key) DO NOTHING`, phone, msg, consent, campaign, key)
        if err != nil {
            skipped++
            reasons["database insert failed"]++
            if firstDatabaseError == "" {
                firstDatabaseError = err.Error()
            }
            continue
        }
        n, _ := res.RowsAffected()
        if n == 0 {
            skipped++
            reasons["duplicate"]++
            continue
        }
        imported++
    }

    out := map[string]any{"status": "success", "campaign_id": campaign, "imported": imported, "skipped": skipped, "skip_reasons": reasons}
    if firstDatabaseError != "" {
        out["first_database_error"] = firstDatabaseError
    }
    bulkJSON(w, 200, out)
}

func init() { go bulkWorkerLoop() }

func bulkAdminPageHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        w.WriteHeader(405)
        return
    }
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    _, _ = w.Write([]byte(`<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"><title>88Task • Bulk Messages</title><style>body{font:14px system-ui;margin:0;color:#111}.wrap{max-width:1000px;margin:auto;padding:24px}.card{border:1px solid #ddd;border-radius:12px;padding:16px;margin:12px 0}.btn{padding:9px 12px;border:1px solid #ccc;border-radius:8px;background:#fff;font-weight:700;margin:4px}.primary{background:#111;color:#fff}.acct{display:inline-flex;gap:7px;margin:6px 14px 6px 0}.muted{color:#777}pre{white-space:pre-wrap;background:#f6f6f6;padding:12px;border-radius:8px}</style></head><body><main class="wrap"><h1>Bulk Messages</h1><p class="muted">CSV columns: phone, message, consent. Consent must be yes/true/1.</p><div class="card"><h3>Connected WhatsApp accounts</h3><div id="accounts">Loading…</div></div><div class="card"><input id="f" type="file" accept=".csv"><button class="btn primary" onclick="u()">Import CSV</button><button class="btn" onclick="s()">Send Selected</button><button class="btn" onclick="a('pause')">Pause</button><button class="btn" onclick="a('resume')">Resume</button><button class="btn" onclick="a('retry_failed')">Retry Failed</button><button class="btn" onclick="a('cancel')">Cancel</button><button class="btn" onclick="a('clear_queue')">Clear Queue</button></div><div class="card"><button class="btn" onclick="load()">Refresh Status</button><pre id="o">Loading…</pre></div></main><script>const H=()=>({'X-Admin-Token':sessionStorage.getItem('admin_token')||''});async function api(url,opt={}){opt.headers={...(opt.headers||{}),...H()};let r=await fetch(url,opt),t=await r.text(),d={};try{d=t?JSON.parse(t):{}}catch(e){throw Error('Invalid server response ('+r.status+')')};if(!r.ok)throw Error(d.message||('Request failed ('+r.status+')'));return d}async function load(){try{let d=await api('/admin/bulk/status');o.textContent=JSON.stringify(d,null,2);accounts.innerHTML=(d.devices||[]).map(function(x){return '<label class="acct"><input type="checkbox" value="'+esc(x.user_id)+'" checked> '+esc(x.phone||x.user_id)+'</label>'}).join('')||'<span class="muted">No connected accounts.</span>'}catch(e){o.textContent=e.message}}async function u(){let f=document.getElementById('f').files[0];if(!f)return alert('Select a CSV file');let x=new FormData();x.append('file',f);try{o.textContent=JSON.stringify(await api('/admin/bulk/import',{method:'POST',body:x}),null,2);load()}catch(e){o.textContent=e.message}}async function a(x){try{o.textContent=JSON.stringify(await api('/admin/bulk/import',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:x})}),null,2);load()}catch(e){o.textContent=e.message}}async function s(){let ids=[...document.querySelectorAll('#accounts input:checked')].map(x=>x.value);if(!ids.length)return alert('Select at least one connected account');try{o.textContent=JSON.stringify(await api('/admin/bulk/import',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'send',user_ids:ids,limit:200})}),null,2);load()}catch(e){o.textContent=e.message}}function esc(v){return String(v??'').replace(/[&<>'\"]/g,function(c){return {'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]})}if(!sessionStorage.getItem('admin_token'))location.href='/admin';load()</script></body></html>`))
}
