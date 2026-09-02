package main

import (
 "encoding/csv"
 "encoding/json"
 "io"
 "net/http"
 "sort"
 "strings"
 "sync/atomic"
 "time"
 "go.mau.fi/whatsmeow/types"
)

var bulkSenderCursor uint64

func ensureBulkMessageTable() error {
 _, err := userDB.Exec(`CREATE TABLE IF NOT EXISTS public.bulk_messages(id BIGSERIAL PRIMARY KEY,user_id TEXT,target TEXT NOT NULL,message TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'queued',attempts INTEGER NOT NULL DEFAULT 0,last_error TEXT,assigned_sender TEXT,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),sent_at TIMESTAMPTZ); ALTER TABLE public.bulk_messages ADD COLUMN IF NOT EXISTS assigned_sender TEXT; ALTER TABLE public.bulk_messages ALTER COLUMN user_id DROP NOT NULL; CREATE INDEX IF NOT EXISTS bulk_messages_queue_idx ON public.bulk_messages(status,id)`)
 return err
}

func bulkConnectedDevices() []string {
 manager.mu.RLock(); defer manager.mu.RUnlock()
 ids:=make([]string,0,len(manager.sessions))
 for uid,s:=range manager.sessions { if s!=nil&&s.client!=nil&&s.client.IsLoggedIn()&&s.client.IsConnected(){ids=append(ids,uid)} }
 sort.Strings(ids)
 return ids
}

func bulkMessageImportHandler(w http.ResponseWriter,r *http.Request){
 enableCORS(w); w.Header().Set("Content-Type","application/json")
 if r.Method!=http.MethodPost {w.WriteHeader(http.StatusMethodNotAllowed);return}
 if err:=ensureBulkMessageTable();err!=nil {w.WriteHeader(http.StatusInternalServerError);return}
 if strings.HasPrefix(r.Header.Get("Content-Type"),"application/json") {
  var in struct{Action string `json:"action"`;UserIDs []string `json:"user_ids"`;Limit int `json:"limit"`}
  if err:=json.NewDecoder(r.Body).Decode(&in);err!=nil {w.WriteHeader(http.StatusBadRequest);_=json.NewEncoder(w).Encode(APIResponse{Status:"error",Message:"Invalid request"});return}
  if in.Action!="send" {w.WriteHeader(http.StatusBadRequest);_=json.NewEncoder(w).Encode(APIResponse{Status:"error",Message:"Unknown action"});return}
  limit:=in.Limit;if limit<1{limit=20};if limit>200{limit=200}
  allowed:=make(map[string]bool)
  if len(in.UserIDs)==0 {for _,uid:=range bulkConnectedDevices(){allowed[uid]=true}} else {for _,uid:=range in.UserIDs{uid=strings.TrimSpace(uid);s:=getSession(uid);if s!=nil&&s.client!=nil&&s.client.IsLoggedIn()&&s.client.IsConnected(){allowed[uid]=true}}}
  if len(allowed)==0 {w.WriteHeader(http.StatusServiceUnavailable);_=json.NewEncoder(w).Encode(APIResponse{Status:"error",Message:"No connected WhatsApp accounts selected"});return}
  sent,attempted:=0,0
  for attempted<limit {uid:=nextBulkSender(allowed);id,target,text,ok:=claimBulkRow(uid);if !ok{break};attempted++;s:=getSession(uid);if s==nil||s.client==nil{markBulkFailed(id,"WhatsApp account disconnected");continue};s.mu.Lock();err:=safeSendMessage(uid,s.client,types.JID{User:target,Server:types.DefaultUserServer},text);s.mu.Unlock();if err!=nil{markBulkFailed(id,err.Error());continue};_,_=userDB.Exec(`UPDATE public.bulk_messages SET status='sent',sent_at=now(),last_error=NULL WHERE id=$1`,id);sent++}
  _=json.NewEncoder(w).Encode(map[string]any{"status":"success","attempted":attempted,"sent":sent,"message":"Manual bulk send completed with per-account safety controls."});return
 }
 r.Body=http.MaxBytesReader(w,r.Body,512<<20);mr,err:=r.MultipartReader();if err!=nil{w.WriteHeader(http.StatusBadRequest);return}
 var file io.Reader
 for {part,e:=mr.NextPart();if e==io.EOF{break};if e!=nil{w.WriteHeader(http.StatusBadRequest);return};if part.FormName()=="file"{file=part;break}}
 if file==nil{w.WriteHeader(http.StatusBadRequest);_=json.NewEncoder(w).Encode(APIResponse{Status:"error",Message:"CSV file is required"});return}
 reader:=csv.NewReader(file);reader.FieldsPerRecord=-1
 insert,err:=userDB.Prepare(`INSERT INTO public.bulk_messages(user_id,target,message,status) VALUES(NULL,$1,$2,'queued')`);if err!=nil{w.WriteHeader(http.StatusInternalServerError);return};defer insert.Close()
 imported,skipped:=0,0;first:=true
 for {rec,e:=reader.Read();if e==io.EOF{break};if e!=nil{skipped++;continue};if first{first=false;if len(rec)>0&&strings.EqualFold(strings.TrimSpace(rec[0]),"phone"){continue}};if len(rec)<3{skipped++;continue};phone:=strings.TrimSpace(strings.TrimPrefix(rec[0],"+"));message:=strings.TrimSpace(rec[1]);consent:=strings.ToLower(strings.TrimSpace(rec[2]));if consent!="yes"&&consent!="true"&&consent!="1"{skipped++;continue};if phone==""||message==""||len(phone)>20||len(message)>10000{skipped++;continue};if _,e=insert.Exec(phone,message);e!=nil{skipped++;continue};imported++ }
 _=json.NewEncoder(w).Encode(map[string]any{"status":"success","imported":imported,"skipped":skipped,"message":"Global messages queued. Safety limits remain enforced per WhatsApp account."})
}

func bulkMessageStatusHandler(w http.ResponseWriter,r *http.Request){
 enableCORS(w);w.Header().Set("Content-Type","application/json");if r.Method!=http.MethodGet{w.WriteHeader(http.StatusMethodNotAllowed);return};if err:=ensureBulkMessageTable();err!=nil{w.WriteHeader(http.StatusInternalServerError);return}
 // Recover jobs left in sending by a crashed worker after 15 minutes.
 _,_=userDB.Exec(`UPDATE public.bulk_messages SET status='queued',assigned_sender=NULL,last_error='Recovered after worker timeout' WHERE user_id IS NULL AND status='sending' AND created_at < now() - interval '15 minutes'`)
 var queued,sending,sent,failed int;_=userDB.QueryRow(`SELECT count(*) FROM public.bulk_messages WHERE user_id IS NULL AND status='queued'`).Scan(&queued);_=userDB.QueryRow(`SELECT count(*) FROM public.bulk_messages WHERE user_id IS NULL AND status='sending'`).Scan(&sending);_=userDB.QueryRow(`SELECT count(*) FROM public.bulk_messages WHERE user_id IS NULL AND status='sent'`).Scan(&sent);_=userDB.QueryRow(`SELECT count(*) FROM public.bulk_messages WHERE user_id IS NULL AND status='failed'`).Scan(&failed)
 devices:=make([]DeviceInfo,0);for _,uid:=range bulkConnectedDevices(){s:=getSession(uid);if s!=nil&&s.client!=nil&&s.client.Store!=nil&&s.client.Store.ID!=nil{devices=append(devices,DeviceInfo{UserID:uid,Phone:s.client.Store.ID.User,Connected:true,LoggedIn:true,State:"ready"})}}
 _=json.NewEncoder(w).Encode(map[string]any{"status":"success","queued":queued,"sending":sending,"sent":sent,"failed":failed,"connected_accounts":len(devices),"devices":devices})
}

func nextBulkSender(allowed map[string]bool) string {ids:=make([]string,0,len(allowed));for id:=range allowed{ids=append(ids,id)};if len(ids)==0{return ""};sort.Strings(ids);n:=atomic.AddUint64(&bulkSenderCursor,1)-1;return ids[n%uint64(len(ids))]}

func claimBulkRow(uid string)(int64,string,string,bool){tx,err:=userDB.Begin();if err!=nil{return 0,"","",false};defer tx.Rollback();var id int64;var target,text string;err=tx.QueryRow(`SELECT id,target,message FROM public.bulk_messages WHERE user_id IS NULL AND status='queued' ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id,&target,&text);if err!=nil{return 0,"","",false};if _,err=tx.Exec(`UPDATE public.bulk_messages SET status='sending',attempts=attempts+1,assigned_sender=$1 WHERE id=$2`,uid,id);err!=nil{return 0,"","",false};if err=tx.Commit();err!=nil{return 0,"","",false};return id,target,text,true}
func markBulkFailed(id int64,msg string){_,_=userDB.Exec(`UPDATE public.bulk_messages SET status='failed',last_error=$1 WHERE id=$2`,msg,id)}

func processBulkMessagesGlobal(){if getAdminSetting("bulk_auto_send_enabled","false")!="true"{return};ids:=bulkConnectedDevices();if len(ids)==0{return};limit:=safeSettingInt("bulk_batch_size",5,1,20);allowed:=make(map[string]bool,len(ids));for _,uid:=range ids{allowed[uid]=true};for i:=0;i<limit;i++{uid:=nextBulkSender(allowed);id,target,text,ok:=claimBulkRow(uid);if !ok{return};s:=getSession(uid);if s==nil||s.client==nil{markBulkFailed(id,"WhatsApp account disconnected");continue};s.mu.Lock();err:=safeSendMessage(uid,s.client,types.JID{User:target,Server:types.DefaultUserServer},text);s.mu.Unlock();if err!=nil{markBulkFailed(id,err.Error());continue};_,_=userDB.Exec(`UPDATE public.bulk_messages SET status='sent',sent_at=now(),last_error=NULL WHERE id=$1`,id)}}

func bulkWorkerLoop(){for userDB==nil{time.Sleep(2*time.Second)};_=ensureBulkMessageTable();t:=time.NewTicker(time.Minute);defer t.Stop();for{processBulkMessagesGlobal();<-t.C}}
func init(){go bulkWorkerLoop()}

func bulkAdminPageHandler(w http.ResponseWriter,r *http.Request){if r.Method!=http.MethodGet{w.WriteHeader(http.StatusMethodNotAllowed);return};data:=`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>88Task • Bulk Messages</title><style>body{font-family:system-ui,sans-serif;background:#fff;color:#111;margin:0}.wrap{max-width:1100px;margin:auto;padding:24px 16px 50px}.card{border:1px solid #ddd;border-radius:14px;padding:20px;margin:16px 0}.muted{color:#555;font-size:13px}.top{display:flex;justify-content:space-between;gap:10px}.title{margin:4px 0}.devices{display:grid;grid-template-columns:repeat(auto-fit,minmax(250px,1fr));gap:10px}.device{border:1px solid #ccc;border-radius:10px;padding:14px}.stats{display:grid;grid-template-columns:repeat(5,1fr);gap:10px}.stat{border:1px solid #ddd;padding:14px;border-radius:10px}.num{display:block;font-size:24px;font-weight:800;margin-top:5px}button{padding:11px 15px;border-radius:8px;border:1px solid #111;background:#111;color:#fff;font-weight:700}button.secondary{background:#fff;color:#111}input[type=file]{padding:8px;max-width:100%}@media(max-width:700px){.stats{grid-template-columns:repeat(2,1fr)}}a{color:#111;font-weight:700}</style></head><body><main class="wrap"><div class="top"><div><a href="/admin">← Admin</a><h1 class="title">Bulk Messages</h1><div class="muted">Global campaign queue</div></div><button class="secondary" onclick="refresh()">Refresh</button></div><div class="card"><h2>Import Campaign Data</h2><div class="muted">Upload CSV: phone,message,consent. Consent must be yes, true, or 1.</div><p><input id="file" type="file" accept=".csv,text/csv"> <button onclick="upload()">Import CSV</button></p><div id="msg" class="muted">Ready.</div></div><div class="card"><h2>Manual Send</h2><div class="muted">Select connected accounts. Messages are distributed across selected accounts. Safety controls remain active.</div><div id="devices" class="devices"></div><p><button class="secondary" onclick="all()">Select All</button> <button class="secondary" onclick="none()">Clear</button> <button onclick="send()">Send Selected</button></p></div><div class="card"><h2>Queue Overview</h2><div class="stats"><div class="stat">Queued<span id="q" class="num">—</span></div><div class="stat">Sending<span id="sg" class="num">—</span></div><div class="stat">Sent<span id="s" class="num">—</span></div><div class="stat">Failed<span id="f" class="num">—</span></div><div class="stat">Connected<span id="c" class="num">—</span></div></div></div></main><script>const H={'X-Admin-Token':sessionStorage.getItem('admin_token')||''};async function api(u,o={}){o.headers=Object.assign({},H,o.headers||{});let r=await fetch(u,o);if(r.status===401||r.status===403){location.href='/admin';throw 0}return r.json()}async function upload(){let f=file.files[0];if(!f){msg.textContent='Select CSV first';return}let fd=new FormData();fd.append('file',f);let d=await api('/admin/bulk/import',{method:'POST',body:fd});msg.textContent=d.message||'Import complete';refresh()}async function send(){let ids=[...document.querySelectorAll('#devices input:checked')].map(x=>x.value);if(!ids.length){msg.textContent='Select at least one account';return}let d=await api('/admin/bulk/import',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'send',user_ids:ids,limit:200})});msg.textContent=d.message||('Sent '+d.sent);refresh()}function all(){document.querySelectorAll('#devices input').forEach(x=>x.checked=true)}function none(){document.querySelectorAll('#devices input').forEach(x=>x.checked=false)}async function refresh(){let d=await api('/admin/bulk/status');q.textContent=d.queued;sg.textContent=d.sending;s.textContent=d.sent;f.textContent=d.failed;c.textContent=d.connected_accounts;devices.innerHTML=d.devices.map(x=>'<label class="device"><input type="checkbox" value="'+x.user_id+'"> '+x.phone+'<br><span class="muted">Connected</span></label>').join('')||'<div class="muted">No connected accounts.</div>'}refresh();setInterval(refresh,10000)</script></body></html>`;w.Write([]byte(data))}
