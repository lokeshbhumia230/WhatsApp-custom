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

func ensureBulkMessageTable() error {
 stmts:=[]string{
  `CREATE TABLE IF NOT EXISTS public.bulk_messages(id BIGSERIAL PRIMARY KEY,user_id TEXT,target TEXT NOT NULL,message TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'queued',attempts INTEGER NOT NULL DEFAULT 0,last_error TEXT,assigned_sender TEXT,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),sent_at TIMESTAMPTZ,campaign_id TEXT,dedupe_key TEXT)`,
  `ALTER TABLE public.bulk_messages ADD COLUMN IF NOT EXISTS user_id TEXT`,
  `ALTER TABLE public.bulk_messages ADD COLUMN IF NOT EXISTS phone TEXT`,
  `ALTER TABLE public.bulk_messages ADD COLUMN IF NOT EXISTS target TEXT`,
  `ALTER TABLE public.bulk_messages ADD COLUMN IF NOT EXISTS message TEXT`,
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
 }
 for _,q:=range stmts { if _,e:=userDB.Exec(q); e!=nil{return e} }
 if _,e:=userDB.Exec(`UPDATE public.bulk_messages SET target=phone WHERE (target IS NULL OR target='') AND phone IS NOT NULL AND phone<>''`);e!=nil{return e}
 if _,e:=userDB.Exec(`UPDATE public.bulk_messages SET status='queued' WHERE status IS NULL OR status=''`);e!=nil{return e}
 if _,e:=userDB.Exec(`UPDATE public.bulk_messages SET attempts=0 WHERE attempts IS NULL`);e!=nil{return e}
 if _,e:=userDB.Exec(`UPDATE public.bulk_messages SET created_at=now() WHERE created_at IS NULL`);e!=nil{return e}
 if _,e:=userDB.Exec(`UPDATE public.bulk_messages SET updated_at=now() WHERE updated_at IS NULL`);e!=nil{return e}
 if _,e:=userDB.Exec(`DELETE FROM public.bulk_messages a USING public.bulk_messages b WHERE a.dedupe_key IS NOT NULL AND a.dedupe_key=b.dedupe_key AND a.id>b.id`);e!=nil{return e}
 if _,e:=userDB.Exec(`CREATE INDEX IF NOT EXISTS bulk_messages_queue_idx ON public.bulk_messages(status,id)`);e!=nil{return e}
 if _,e:=userDB.Exec(`CREATE INDEX IF NOT EXISTS bulk_messages_sender_idx ON public.bulk_messages(assigned_sender,status)`);e!=nil{return e}
 if _,e:=userDB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS bulk_messages_dedupe_idx ON public.bulk_messages(dedupe_key) WHERE dedupe_key IS NOT NULL`);e!=nil{return e}
 return nil
}

func bulkConnectedDevices()[]string{manager.mu.RLock();defer manager.mu.RUnlock();ids:=[]string{};for id,s:=range manager.sessions{if s!=nil&&s.client!=nil&&s.client.IsLoggedIn()&&s.client.IsConnected(){ids=append(ids,id)}};sort.Strings(ids);return ids}
func normalizeBulkPhone(v string)(string,bool){v=strings.NewReplacer("+",""," ","","-","").Replace(strings.TrimSpace(v));if len(v)<7||len(v)>15{return "",false};for _,c:=range v{if c<'0'||c>'9'{return "",false}};return v,true}
func bulkKey(c,p,m string)string{h:=sha256.Sum256([]byte(c+"\x00"+p+"\x00"+m));return hex.EncodeToString(h[:])}
func bulkQueuedCount()int{var n int;_=userDB.QueryRow(`SELECT count(*) FROM public.bulk_messages WHERE user_id IS NULL AND status='queued'`).Scan(&n);return n}
func claimBulkRow(uid string)(int64,string,string,bool){tx,e:=userDB.Begin();if e!=nil{return 0,"","",false};defer tx.Rollback();var id int64;var target,text string;if e=tx.QueryRow(`SELECT id,target,message FROM public.bulk_messages WHERE user_id IS NULL AND status='queued' AND target IS NOT NULL AND target<>'' ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id,&target,&text);e!=nil{return 0,"","",false};if _,e=tx.Exec(`UPDATE public.bulk_messages SET status='sending',attempts=attempts+1,assigned_sender=$1,updated_at=now() WHERE id=$2`,uid,id);e!=nil{return 0,"","",false};if e=tx.Commit();e!=nil{return 0,"","",false};return id,target,text,true}
func markBulkFailed(id int64,msg string){_,_=userDB.Exec(`UPDATE public.bulk_messages SET status='failed',last_error=$1,updated_at=now() WHERE id=$2`,msg,id)}
func markBulkSent(id int64){_,_=userDB.Exec(`UPDATE public.bulk_messages SET status='sent',sent_at=now(),last_error=NULL,updated_at=now() WHERE id=$1`,id)}
func bulkSenderForIndex(ids []string,i int)string{if len(ids)==0{return ""};return ids[i%len(ids)]}
func processBulkMessagesGlobal(){if getAdminSetting("bulk_auto_send_enabled","false")!="true"{return};ids:=bulkConnectedDevices();if len(ids)==0{return};limit:=safeSettingInt("bulk_batch_size",5,1,20);for i:=0;i<limit;i++{uid:=bulkSenderForIndex(ids,i);id,target,text,ok:=claimBulkRow(uid);if !ok{return};s:=getSession(uid);if s==nil||s.client==nil{markBulkFailed(id,"WhatsApp account disconnected");continue};s.mu.Lock();e:=safeSendMessage(uid,s.client,types.JID{User:target,Server:types.DefaultUserServer},text);s.mu.Unlock();if e!=nil{markBulkFailed(id,e.Error())}else{markBulkSent(id)}}}
func bulkWorkerLoop(){for userDB==nil{time.Sleep(2*time.Second)};if e:=ensureBulkMessageTable();e!=nil{println("bulk table init error:",e.Error())};for{processBulkMessagesGlobal();time.Sleep(10*time.Second)}}
func bulkJSON(w http.ResponseWriter,status int,v any){w.Header().Set("Content-Type","application/json; charset=utf-8");w.WriteHeader(status);_=json.NewEncoder(w).Encode(v)}

func bulkMessageStatusHandler(w http.ResponseWriter,r *http.Request){enableCORS(w);if r.Method!=http.MethodGet{bulkJSON(w,405,APIResponse{Status:"error",Message:"Method not allowed"});return};if e:=ensureBulkMessageTable();e!=nil{bulkJSON(w,500,APIResponse{Status:"error",Message:"Bulk table initialization failed: "+e.Error()});return};_,_=userDB.Exec(`UPDATE public.bulk_messages SET status='queued',assigned_sender=NULL,last_error='Recovered after worker timeout',updated_at=now() WHERE user_id IS NULL AND status='sending' AND updated_at < now()-interval '15 minutes'`);out:=map[string]any{"status":"success"};for _,st:=range []string{"queued","sending","sent","failed","paused","cancelled"}{var n int;_=userDB.QueryRow(`SELECT count(*) FROM public.bulk_messages WHERE user_id IS NULL AND status=$1`,st).Scan(&n);out[st]=n};devices:=[]DeviceInfo{};for _,uid:=range bulkConnectedDevices(){s:=getSession(uid);if s!=nil&&s.client!=nil&&s.client.Store!=nil&&s.client.Store.ID!=nil{devices=append(devices,DeviceInfo{UserID:uid,Phone:s.client.Store.ID.User,Connected:true,LoggedIn:true,State:"ready"})}};out["connected_accounts"]=len(devices);out["devices"]=devices;bulkJSON(w,200,out)}

func bulkMessageImportHandler(w http.ResponseWriter,r *http.Request){
 enableCORS(w);if r.Method!=http.MethodPost{bulkJSON(w,405,APIResponse{Status:"error",Message:"Method not allowed"});return}
 if e:=ensureBulkMessageTable();e!=nil{bulkJSON(w,500,APIResponse{Status:"error",Message:"Bulk table initialization failed: "+e.Error()});return}
 if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")),"application/json"){
  var in struct{Action string `json:"action"`;UserIDs []string `json:"user_ids"`;Limit int `json:"limit"`};if e:=json.NewDecoder(r.Body).Decode(&in);e!=nil{bulkJSON(w,400,APIResponse{Status:"error",Message:"Invalid JSON: "+e.Error()});return};a:=strings.ToLower(strings.TrimSpace(in.Action))
  if a!="send"{q:=map[string]string{"pause":"UPDATE public.bulk_messages SET status='paused',updated_at=now() WHERE user_id IS NULL AND status='queued'","resume":"UPDATE public.bulk_messages SET status='queued',updated_at=now() WHERE user_id IS NULL AND status='paused'","cancel":"UPDATE public.bulk_messages SET status='cancelled',updated_at=now() WHERE user_id IS NULL AND status IN ('queued','paused','failed')","retry_failed":"UPDATE public.bulk_messages SET status='queued',attempts=0,last_error=NULL,assigned_sender=NULL,updated_at=now() WHERE user_id IS NULL AND status='failed'","clear_queue":"DELETE FROM public.bulk_messages WHERE user_id IS NULL AND status IN ('queued','paused','cancelled','failed')"};sql,ok:=q[a];if !ok{bulkJSON(w,400,APIResponse{Status:"error",Message:"Unknown action"});return};res,e:=userDB.Exec(sql);if e!=nil{bulkJSON(w,500,APIResponse{Status:"error",Message:e.Error()});return};n,_:=res.RowsAffected();bulkJSON(w,200,map[string]any{"status":"success","action":a,"affected":n});return}
  limit:=in.Limit;if limit<1{limit=20};if limit>200{limit=200};ids:=in.UserIDs;if len(ids)==0{ids=bulkConnectedDevices()};valid:=map[string]bool{};for _,id:=range bulkConnectedDevices(){valid[id]=true};selected:=[]string{};for _,id:=range ids{if valid[id]{selected=append(selected,id)}};if len(selected)==0{bulkJSON(w,400,APIResponse{Status:"error",Message:"No connected WhatsApp accounts selected"});return};sent,attempted:=0,0;for attempted<limit{uid:=bulkSenderForIndex(selected,attempted);id,target,text,ok:=claimBulkRow(uid);if !ok{break};attempted++;s:=getSession(uid);if s==nil||s.client==nil{markBulkFailed(id,"WhatsApp account disconnected");continue};s.mu.Lock();e:=safeSendMessage(uid,s.client,types.JID{User:target,Server:types.DefaultUserServer},text);s.mu.Unlock();if e!=nil{markBulkFailed(id,e.Error())}else{markBulkSent(id);sent++}};bulkJSON(w,200,map[string]any{"status":"success","attempted":attempted,"sent":sent,"remaining_queue":bulkQueuedCount()});return
 }
 r.Body=http.MaxBytesReader(w,r.Body,512<<20);mr,e:=r.MultipartReader();if e!=nil{bulkJSON(w,400,APIResponse{Status:"error",Message:"Invalid multipart upload: "+e.Error()});return};var file io.Reader;for{p,x:=mr.NextPart();if x==io.EOF{break};if x!=nil{bulkJSON(w,400,APIResponse{Status:"error",Message:"Invalid multipart data: "+x.Error()});return};if p.FormName()=="file"{file=p;break}};if file==nil{bulkJSON(w,400,APIResponse{Status:"error",Message:"CSV file is required"});return}
 reader:=csv.NewReader(file);reader.FieldsPerRecord=-1;campaign:=time.Now().UTC().Format("20060102T150405.000000000Z");imported,skipped:=0,0;first:=true
 for{rec,x:=reader.Read();if x==io.EOF{break};if x!=nil{skipped++;continue};if first{first=false;if len(rec)>0&&strings.EqualFold(strings.TrimSpace(rec[0]),"phone"){continue}};if len(rec)<3{skipped++;continue};phone,ok:=normalizeBulkPhone(rec[0]);msg:=strings.TrimSpace(rec[1]);cons:=strings.ToLower(strings.TrimSpace(rec[2]));if !ok||msg==""||len(msg)>10000||(cons!="yes"&&cons!="true"&&cons!="1"){skipped++;continue};key:=bulkKey(campaign,phone,msg)
  // Do not use one long transaction here. A single malformed/legacy row must not abort the entire PostgreSQL transaction and turn Commit into an unexpected rollback.
  res,x:=userDB.Exec(`INSERT INTO public.bulk_messages(user_id,target,message,status,campaign_id,dedupe_key,updated_at) VALUES(NULL,$1,$2,'queued',$3,$4,now()) ON CONFLICT(dedupe_key) DO NOTHING`,phone,msg,campaign,key);if x!=nil{skipped++;continue};n,_:=res.RowsAffected();if n==0{skipped++;continue};imported++
 }
 bulkJSON(w,200,map[string]any{"status":"success","campaign_id":campaign,"imported":imported,"skipped":skipped})
}

func init(){go bulkWorkerLoop()}

func bulkAdminPageHandler(w http.ResponseWriter,r *http.Request){if r.Method!=http.MethodGet{w.WriteHeader(405);return};w.Header().Set("Content-Type","text/html; charset=utf-8");_,_=w.Write([]byte(`<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"><title>88Task • Bulk Messages</title><style>body{font:14px system-ui;margin:0;color:#111}.wrap{max-width:1000px;margin:auto;padding:24px}.card{border:1px solid #ddd;border-radius:12px;padding:16px;margin:12px 0}.btn{padding:9px 12px;border:1px solid #ccc;border-radius:8px;background:#fff;font-weight:700;margin:4px}.primary{background:#111;color:#fff}.acct{display:inline-flex;gap:7px;margin:6px 14px 6px 0}.muted{color:#777}pre{white-space:pre-wrap;background:#f6f6f6;padding:12px;border-radius:8px}</style></head><body><main class="wrap"><h1>Bulk Messages</h1><p class="muted">Global CSV queue. Select connected WhatsApp accounts for manual sending. Automatic sending uses the same safety controls.</p><div class="card"><h3>Connected WhatsApp accounts</h3><div id="accounts">Loading…</div></div><div class="card"><input id="f" type="file" accept=".csv"><button class="btn primary" onclick="u()">Import CSV</button><button class="btn" onclick="s()">Send Selected</button><button class="btn" onclick="a('pause')">Pause</button><button class="btn" onclick="a('resume')">Resume</button><button class="btn" onclick="a('retry_failed')">Retry Failed</button><button class="btn" onclick="a('cancel')">Cancel</button><button class="btn" onclick="a('clear_queue')">Clear Queue</button></div><div class="card"><button class="btn" onclick="load()">Refresh Status</button><pre id="o">Loading…</pre></div></main><script>const H=()=>({'X-Admin-Token':sessionStorage.getItem('admin_token')||''});async function api(url,opt={}){opt.headers={...(opt.headers||{}),...H()};let r=await fetch(url,opt),t=await r.text(),d={};try{d=t?JSON.parse(t):{}}catch(e){throw Error('Invalid server response ('+r.status+')')};if(!r.ok)throw Error(d.message||('Request failed ('+r.status+')'));return d}async function load(){try{let d=await api('/admin/bulk/status');o.textContent=JSON.stringify(d,null,2);accounts.innerHTML=(d.devices||[]).map(function(x){return '<label class="acct"><input type="checkbox" value="'+esc(x.user_id)+'" checked> '+esc(x.phone||x.user_id)+'</label>'}).join('')||'<span class="muted">No connected accounts.</span>'}catch(e){o.textContent=e.message}}async function u(){let f=document.getElementById('f').files[0];if(!f)return alert('Select a CSV file');let x=new FormData();x.append('file',f);try{o.textContent=JSON.stringify(await api('/admin/bulk/import',{method:'POST',body:x}),null,2);load()}catch(e){o.textContent=e.message}}async function a(x){try{o.textContent=JSON.stringify(await api('/admin/bulk/import',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:x})}),null,2);load()}catch(e){o.textContent=e.message}}async function s(){let ids=[...document.querySelectorAll('#accounts input:checked')].map(x=>x.value);if(!ids.length)return alert('Select at least one connected account');try{o.textContent=JSON.stringify(await api('/admin/bulk/import',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'send',user_ids:ids,limit:200})}),null,2);load()}catch(e){o.textContent=e.message}}function esc(v){return String(v??'').replace(/[&<>'\"]/g,function(c){return {'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','\"':'&quot;'}[c]})}if(!sessionStorage.getItem('admin_token'))location.href='/admin';load()</script></body></html>`))}
