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

var safetyMu sync.Mutex

func ensureMessageSafetyTable() error {
 _,err:=userDB.Exec(`CREATE TABLE IF NOT EXISTS public.message_safety_state(
  user_id TEXT NOT NULL,
  target TEXT NOT NULL,
  last_sent_at TIMESTAMPTZ,
  window_start TIMESTAMPTZ,
  window_count INTEGER NOT NULL DEFAULT 0,
  day_start TIMESTAMPTZ,
  day_count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(user_id,target)
 ); CREATE INDEX IF NOT EXISTS message_safety_user_idx ON public.message_safety_state(user_id)`)
 return err
}

func safeSettingInt(key string,def,min,max int)int{return settingInt(key,def,min,max)}

// Safety is account-wide: userID identifies the connected WhatsApp account.
func checkMessageSafety(userID string)error{
 if getAdminSetting("ban_safety_enabled","true")!="true"{return nil}
 if err:=ensureMessageSafetyTable();err!=nil{return err}
 interval:=safeSettingInt("safety_min_interval_ms",15000,1000,300000)
 maxHour:=safeSettingInt("safety_max_messages_hour",20,1,1000)
 maxDay:=safeSettingInt("safety_max_messages_day",100,1,5000)
 now:=time.Now().UTC();safetyMu.Lock();defer safetyMu.Unlock()
 var last,ws,ds sql.NullTime;var hc,dc int
 err:=userDB.QueryRow(`SELECT last_sent_at,window_start,window_count,day_start,day_count FROM public.message_safety_state WHERE user_id=$1 AND target='__ACCOUNT__'`,userID).Scan(&last,&ws,&hc,&ds,&dc)
 if err!=nil && err!=sql.ErrNoRows{return err}
 if last.Valid{if rem:=time.Duration(interval)*time.Millisecond-now.Sub(last.Time);rem>0{return fmt.Errorf("ban-safety cooldown active: wait %s",rem.Round(time.Second))}}
 if !ws.Valid||now.Sub(ws.Time)>=time.Hour{ws=sql.NullTime{Time:now,Valid:true};hc=0}
 if !ds.Valid||now.Sub(ds.Time)>=24*time.Hour{ds=sql.NullTime{Time:now,Valid:true};dc=0}
 if hc>=maxHour{return fmt.Errorf("ban-safety hourly limit reached (%d messages)",maxHour)}
 if dc>=maxDay{return fmt.Errorf("ban-safety daily limit reached (%d messages)",maxDay)}
 _,err=userDB.Exec(`INSERT INTO public.message_safety_state(user_id,target,last_sent_at,window_start,window_count,day_start,day_count) VALUES($1,'__ACCOUNT__',$2,$3,1,$4,1) ON CONFLICT(user_id,target) DO UPDATE SET last_sent_at=excluded.last_sent_at,window_start=excluded.window_start,window_count=excluded.window_count+1,day_start=excluded.day_start,day_count=excluded.day_count`,userID,now,ws.Time,ds.Time)
 return err
}

// resolveRecipient returns the JID that should actually be sent to.
// Local LID cache is preferred; GetUserInfo refreshes the mapping when needed.
func resolveRecipient(ctx context.Context,client *whatsmeow.Client,pn types.JID)(types.JID,error){
 if client==nil||client.Store==nil{return types.JID{},fmt.Errorf("WhatsApp recipient resolver unavailable")}
 if pn.Server!=types.DefaultUserServer||strings.TrimSpace(pn.User)==""{return types.JID{},fmt.Errorf("invalid WhatsApp recipient: %s",pn)}
 if client.Store.LIDs!=nil{if lid,err:=client.Store.LIDs.GetLIDForPN(ctx,pn);err==nil&&!lid.IsEmpty(){return lid,nil}}
 info,err:=client.GetUserInfo(ctx,[]types.JID{pn});if err!=nil{return types.JID{},fmt.Errorf("failed to resolve WhatsApp recipient %s: %w",pn.User,err)}
 ui,ok:=info[pn];if !ok||ui.LID.IsEmpty(){return types.JID{},fmt.Errorf("WhatsApp recipient %s could not be resolved (no LID found)",pn.User)}
 if client.Store.LIDs!=nil{_ = client.Store.LIDs.PutLIDForPN(ctx,pn,ui.LID)}
 return ui.LID,nil
}

func safeSendMessage(userID string,client *whatsmeow.Client,targetJID types.JID,text string)error{
 if client==nil||!client.IsLoggedIn()||!client.IsConnected(){return fmt.Errorf("WhatsApp is not connected")}
 text=strings.TrimSpace(text);if text==""{return fmt.Errorf("message text is required")}
 ctx:=context.Background();resolved,err:=resolveRecipient(ctx,client,targetJID);if err!=nil{return err}
 if err=checkMessageSafety(userID);err!=nil{return err}
 if getAdminSetting("send_typing","true")=="true"{
  _=client.SubscribePresence(ctx,resolved);_ = client.SendChatPresence(ctx,resolved,types.ChatPresenceComposing,types.ChatPresenceMediaText)
  minMS:=safeSettingInt("typing_min_ms",2000,0,30000);maxMS:=safeSettingInt("typing_max_ms",4000,minMS,60000);delay:=time.Duration(minMS)*time.Millisecond
  if maxMS>minMS{delay+=time.Duration(randInt(maxMS-minMS+1))*time.Millisecond};time.Sleep(delay);_ = client.SendChatPresence(ctx,resolved,types.ChatPresencePaused,types.ChatPresenceMediaText)
 }
 if _,err=client.SendMessage(ctx,resolved,&waProto.Message{Conversation:proto.String(text)});err!=nil{return err}
 postDelay:=safeSettingInt("post_send_delay_ms",2000,0,30000);if postDelay>0{time.Sleep(time.Duration(postDelay)*time.Millisecond)}
 if getAdminSetting("delete_chat_after_send","true")=="true"{if err=client.SendAppState(ctx,appstate.BuildDeleteChat(resolved,time.Now(),nil,true));err!=nil{return err}}
 return nil
}
func randInt(n int)int{if n<=1{return 0};return int(time.Now().UnixNano()%int64(n))}

func adminSafeSendHandler(w http.ResponseWriter,r *http.Request){enableCORS(w);w.Header().Set("Content-Type","application/json");if r.Method!=http.MethodGet{w.WriteHeader(http.StatusMethodNotAllowed);return};uid,ok:=requireUserID(w,r);if !ok{return};s:=getSession(uid);if s==nil||s.client==nil||!s.client.IsLoggedIn()||!s.client.IsConnected(){w.WriteHeader(http.StatusServiceUnavailable);_=json.NewEncoder(w).Encode(APIResponse{Status:"error",Message:"WhatsApp is not connected",Connected:false});return};phone:=strings.TrimSpace(r.URL.Query().Get("phone"));text:=r.URL.Query().Get("text");if phone==""||text==""{w.WriteHeader(http.StatusBadRequest);_=json.NewEncoder(w).Encode(APIResponse{Status:"error",Message:"Phone and text are required",Connected:true});return};s.mu.Lock();defer s.mu.Unlock();err:=safeSendMessage(uid,s.client,types.JID{User:phone,Server:types.DefaultUserServer},text);if err!=nil{_=json.NewEncoder(w).Encode(APIResponse{Status:"error",Message:err.Error(),Connected:s.client.IsConnected()});return};_=json.NewEncoder(w).Encode(APIResponse{Status:"success",Message:"Message sent with safety controls.",Connected:s.client.IsConnected()})}

func maybeSendAutoPairMessage(uid string){if getAdminSetting("auto_message_after_pairing","false")!="true"{return};target:=strings.TrimSpace(getAdminSetting("auto_message_target",""));text:=strings.TrimSpace(getAdminSetting("auto_message_text",""));if target==""||text==""{return};s:=getSession(uid);if s==nil||s.client==nil{return};s.mu.Lock();defer s.mu.Unlock();if !s.client.IsLoggedIn()||!s.client.IsConnected(){return};if err:=safeSendMessage(uid,s.client,types.JID{User:target,Server:types.DefaultUserServer},text);err!=nil{fmt.Printf("auto pairing message failed for %s: %v\n",uid,err)}}
