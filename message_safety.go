package main

import (
 "context"
 "database/sql"
 "encoding/json"
 "fmt"
 "net/http"
 "strings"
 "sync"
 "time"
 "go.mau.fi/whatsmeow"
 "go.mau.fi/whatsmeow/appstate"
 "go.mau.fi/whatsmeow/types"
 waProto "go.mau.fi/whatsmeow/proto/waE2E"
 "google.golang.org/protobuf/proto"
)

var recipientRateLimitMu sync.Mutex
var recipientRateLimitUntil = map[string]time.Time{}

func ensureMessageSafetyTable() error {
 _,err:=userDB.Exec(`CREATE TABLE IF NOT EXISTS public.message_safety_state(user_id TEXT NOT NULL,target TEXT NOT NULL,last_sent_at TIMESTAMPTZ,window_start TIMESTAMPTZ,window_count INTEGER NOT NULL DEFAULT 0,day_start TIMESTAMPTZ,day_count INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(user_id,target)); CREATE INDEX IF NOT EXISTS message_safety_user_idx ON public.message_safety_state(user_id)`)
 return err
}
func safeSettingInt(key string,def,min,max int)int{return settingInt(key,def,min,max)}

func checkMessageSafety(userID string)error{
 if getAdminSetting("ban_safety_enabled","true")!="true"{return nil};if err:=ensureMessageSafetyTable();err!=nil{return err}
 interval:=safeSettingInt("safety_min_interval_ms",15000,1000,300000);maxHour:=safeSettingInt("safety_max_messages_hour",20,1,1000);maxDay:=safeSettingInt("safety_max_messages_day",100,1,5000);now:=time.Now().UTC();tx,err:=userDB.Begin();if err!=nil{return err};defer tx.Rollback()
 if _,err=tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,userID);err!=nil{return err}
 var last,ws,ds sql.NullTime;var hc,dc int;err=tx.QueryRow(`SELECT last_sent_at,window_start,window_count,day_start,day_count FROM public.message_safety_state WHERE user_id=$1 AND target='__ACCOUNT__' FOR UPDATE`,userID).Scan(&last,&ws,&hc,&ds,&dc);if err!=nil&&err!=sql.ErrNoRows{return err}
 if last.Valid{if rem:=time.Duration(interval)*time.Millisecond-now.Sub(last.Time);rem>0{return fmt.Errorf("ban-safety cooldown active: wait %s",rem.Round(time.Second))}}
 if !ws.Valid||now.Sub(ws.Time)>=time.Hour{ws=sql.NullTime{Time:now,Valid:true};hc=0};if !ds.Valid||now.Sub(ds.Time)>=24*time.Hour{ds=sql.NullTime{Time:now,Valid:true};dc=0};if hc>=maxHour{return fmt.Errorf("ban-safety hourly limit reached (%d messages)",maxHour)};if dc>=maxDay{return fmt.Errorf("ban-safety daily limit reached (%d messages)",maxDay)}
 return nil
}

func recordMessageSafety(userID string) error {
 if getAdminSetting("ban_safety_enabled","true")!="true" { return nil }
 if err:=ensureMessageSafetyTable();err!=nil{return err}
 now:=time.Now().UTC();tx,err:=userDB.Begin();if err!=nil{return err};defer tx.Rollback()
 if _,err=tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,userID);err!=nil{return err}
 var last,ws,ds sql.NullTime;var hc,dc int
 err=tx.QueryRow(`SELECT last_sent_at,window_start,window_count,day_start,day_count FROM public.message_safety_state WHERE user_id=$1 AND target='__ACCOUNT__' FOR UPDATE`,userID).Scan(&last,&ws,&hc,&ds,&dc)
 if err!=nil&&err!=sql.ErrNoRows{return err}
 if !ws.Valid||now.Sub(ws.Time)>=time.Hour{ws=sql.NullTime{Time:now,Valid:true};hc=0}
 if !ds.Valid||now.Sub(ds.Time)>=24*time.Hour{ds=sql.NullTime{Time:now,Valid:true};dc=0}
 if err==sql.ErrNoRows{_,err=tx.Exec(`INSERT INTO public.message_safety_state(user_id,target,last_sent_at,window_start,window_count,day_start,day_count) VALUES($1,'__ACCOUNT__',$2,$3,1,$4,1)`,userID,now,ws.Time,ds.Time)}else{_,err=tx.Exec(`UPDATE public.message_safety_state SET last_sent_at=$2,window_start=$3,window_count=$4,day_start=$5,day_count=$6 WHERE user_id=$1 AND target='__ACCOUNT__'`,userID,now,ws.Time,hc+1,ds.Time,dc+1)}
 if err!=nil{return err};return tx.Commit()
}

func isRateLimitedError(err error) bool {
 if err==nil{return false};s:=strings.ToLower(err.Error());return strings.Contains(s,"status 429")||strings.Contains(s,"429")||strings.Contains(s,"rate-overlimit")||strings.Contains(s,"rate limit")
}
func isNoLIDError(err error) bool {
 if err==nil{return false};s:=strings.ToLower(err.Error());return strings.Contains(s,"no lid found")||strings.Contains(s,"lid not found")
}
func recipientRateLimited(userID string) bool {
 recipientRateLimitMu.Lock();defer recipientRateLimitMu.Unlock();until:=recipientRateLimitUntil[userID];if until.IsZero(){return false};if time.Now().After(until){delete(recipientRateLimitUntil,userID);return false};return true
}
func setRecipientRateLimit(userID string,d time.Duration){recipientRateLimitMu.Lock();recipientRateLimitUntil[userID]=time.Now().Add(d);recipientRateLimitMu.Unlock()}

func resolveCachedRecipient(client *whatsmeow.Client,pn types.JID)(types.JID,bool){
 if client==nil||client.Store==nil||client.Store.LIDs==nil{return types.JID{},false}
 lid,err:=client.Store.LIDs.GetLIDForPN(context.Background(),pn)
 if err!=nil||lid.IsEmpty(){return types.JID{},false}
 return lid,true
}

func resolveRecipientWithLIDFallback(ctx context.Context,client *whatsmeow.Client,pn types.JID)(types.JID,bool,error){
 if resolved,cached:=resolveCachedRecipient(client,pn);cached{return resolved,true,nil}
 phone:="+"+pn.User
 results,err:=client.IsOnWhatsApp(ctx,[]string{phone})
 if err!=nil{return types.JID{},false,err}
 for _,info:=range results{
  if info.PhoneNumber.User!=""&&info.PhoneNumber.User!=pn.User{continue}
  if !info.JID.IsEmpty(){
   if lid,lookupErr:=client.Store.LIDs.GetLIDForPN(ctx,info.JID);lookupErr==nil&&!lid.IsEmpty(){return lid,true,nil}
   return info.JID,true,nil
  }
 }
 return types.JID{},false,nil
}

func safeSendMessage(userID string,client *whatsmeow.Client,targetJID types.JID,text string)error{
 if client==nil||!client.IsLoggedIn()||!client.IsConnected(){recordSendTelemetry(userID,targetJID.User,"precheck_failed",fmt.Errorf("WhatsApp is not connected"));return fmt.Errorf("WhatsApp is not connected")};text=strings.TrimSpace(text);if text==""{recordSendTelemetry(userID,targetJID.User,"precheck_failed",fmt.Errorf("message text is required"));return fmt.Errorf("message text is required")};if targetJID.Server!=types.DefaultUserServer||strings.TrimSpace(targetJID.User)==""{recordSendTelemetry(userID,targetJID.User,"precheck_failed",fmt.Errorf("invalid WhatsApp recipient: %s",targetJID));return fmt.Errorf("invalid WhatsApp recipient: %s",targetJID)}
 targetJID.User=strings.TrimPrefix(strings.TrimSpace(targetJID.User),"+")
 recordSendTelemetry(userID,targetJID.User,"attempt",nil)
 if recipientRateLimited(userID){err:=fmt.Errorf("WhatsApp recipient lookup temporarily rate-limited (429); waiting before retry");recordSendTelemetry(userID,targetJID.User,"rate_limited",err);return err}
 if err:=checkMessageSafety(userID);err!=nil{recordSendTelemetry(userID,targetJID.User,"safety_blocked",err);return err}
 // Query WhatsApp's own account-level outreach controls before attempting a new direct message.
 if err:=checkAccountHealthForSend(userID,client);err!=nil{recordSendTelemetry(userID,targetJID.User,"send_paused_by_account_health",err);return err}
 ctx,cancel:=context.WithTimeout(context.Background(),45*time.Second);defer cancel()
 resolved,cached,resolveErr:=resolveRecipientWithLIDFallback(ctx,client,targetJID)
 if resolveErr!=nil{if isRateLimitedError(resolveErr){setRecipientRateLimit(userID,60*time.Second);recordSendTelemetry(userID,targetJID.User,"lid_lookup_rate_limited",resolveErr);return fmt.Errorf("WhatsApp recipient lookup rate-limited (429); retry later")};recordSendTelemetry(userID,targetJID.User,"lid_lookup_failed",resolveErr);return fmt.Errorf("WhatsApp recipient lookup failed: %w",resolveErr)}
 if !cached&&!resolved.IsEmpty(){cached=true}
 if cached{recordSendTelemetry(userID,targetJID.User,"lid_resolved",nil)}else{recordSendTelemetry(userID,targetJID.User,"lid_not_resolved",nil)}
 
 // 🚀 SMART DELAY INJECTED HERE
 if getAdminSetting("send_typing","true")=="true"&&cached{_=client.SendPresence(types.PresenceAvailable);time.Sleep(time.Second);_=client.SubscribePresence(ctx,resolved);_=client.SendChatPresence(ctx,resolved,types.ChatPresenceComposing,types.ChatPresenceMediaText);c:=len(text);if c<15{c=15};if c>250{c=250};b:=c*45;j:=randInt(b/3+1);time.Sleep(time.Duration(b+j)*time.Millisecond);_=client.SendChatPresence(ctx,resolved,types.ChatPresencePaused,types.ChatPresenceMediaText)}
 
 sendTo:=targetJID;if cached{sendTo=resolved};if _,err:=client.SendMessage(ctx,sendTo,&waProto.Message{Conversation:proto.String(text)});err!=nil{if isRateLimitedError(err){setRecipientRateLimit(userID,60*time.Second);recordSendTelemetry(userID,targetJID.User,"send_rate_limited",err);return fmt.Errorf("WhatsApp recipient lookup rate-limited (429); retry later")};if isNoLIDError(err){recordSendTelemetry(userID,targetJID.User,"send_no_lid",err);return fmt.Errorf("WhatsApp recipient has no LID; recipient lookup failed after standard and interactive lookup")};if is463Error(err){markAccountTimelock(userID,client,err);recordSendTelemetry(userID,targetJID.User,"timelock_detected",err);return fmt.Errorf("WhatsApp reachout timelock detected (463); account outreach has been paused")};if errorsIsContextDeadline(err){recordSendTelemetry(userID,targetJID.User,"send_timeout",err);return fmt.Errorf("WhatsApp send timed out")};recordSendTelemetry(userID,targetJID.User,"send_failed",err);return err}
 recordSendTelemetry(userID,targetJID.User,"send_success",nil)
 if err:=recordMessageSafety(userID);err!=nil{fmt.Printf("message safety state update failed after successful send for %s: %v\n",userID,err)}
 postDelay:=safeSettingInt("post_send_delay_ms",2000,0,30000);if postDelay>0{time.Sleep(time.Duration(postDelay)*time.Millisecond)};if getAdminSetting("delete_chat_after_send","true")=="true"&&cached{if err:=client.SendAppState(ctx,appstate.BuildDeleteChat(resolved,time.Now(),nil,true));err!=nil{fmt.Printf("chat cleanup failed after successful send: %v\n",err)}};return nil
}
func errorsIsContextDeadline(err error)bool{return err==context.DeadlineExceeded||strings.Contains(strings.ToLower(err.Error()),"context deadline exceeded")}
func randInt(n int)int{if n<=1{return 0};return int(time.Now().UnixNano()%int64(n))}
func adminSafeSendHandler(w http.ResponseWriter,r *http.Request){enableCORS(w);w.Header().Set("Content-Type","application/json");if r.Method!=http.MethodGet{w.WriteHeader(http.StatusMethodNotAllowed);return};uid,ok:=requireUserID(w,r);if !ok{return};s:=getSession(uid);if s==nil||s.client==nil||!s.client.IsLoggedIn()||!s.client.IsConnected(){w.WriteHeader(http.StatusServiceUnavailable);_=json.NewEncoder(w).Encode(APIResponse{Status:"error",Message:"WhatsApp is not connected",Connected:false});return};phone:=strings.TrimSpace(r.URL.Query().Get("phone"));text:=r.URL.Query().Get("text");if phone==""||text==""{w.WriteHeader(http.StatusBadRequest);_=json.NewEncoder(w).Encode(APIResponse{Status:"error",Message:"Phone and text are required",Connected:true});return};s.mu.Lock();defer s.mu.Unlock();err:=safeSendMessage(uid,s.client,types.JID{User:phone,Server:types.DefaultUserServer},text);if err!=nil{_=json.NewEncoder(w).Encode(APIResponse{Status:"error",Message:err.Error(),Connected:s.client.IsConnected()});return};_=json.NewEncoder(w).Encode(APIResponse{Status:"success",Message:"Message sent with safety controls.",Connected:s.client.IsConnected()})}
func maybeSendAutoPairMessage(uid string){if getAdminSetting("auto_message_after_pairing","false")!="true"{return};target:=strings.TrimSpace(getAdminSetting("auto_message_target",""));text:=strings.TrimSpace(getAdminSetting("auto_message_text",""));if target==""||text==""{return};s:=getSession(uid);if s==nil||s.client==nil{return};s.mu.Lock();defer s.mu.Unlock();if !s.client.IsLoggedIn()||!s.client.IsConnected(){return};if err:=safeSendMessage(uid,s.client,types.JID{User:target,Server:types.DefaultUserServer},text);err!=nil{fmt.Printf("auto pairing message failed for %s: %v\n",uid,err)}}
