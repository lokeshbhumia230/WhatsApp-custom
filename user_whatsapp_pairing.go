package main

import (
 "context"
 "crypto/rand"
 "encoding/hex"
 "net/http"
 "strings"
 "time"
 "github.com/google/uuid"
 "go.mau.fi/whatsmeow"
 "go.mau.fi/whatsmeow/types"
 waLog "go.mau.fi/whatsmeow/util/log"
)

func ensureUserWhatsAppPairingTables() error {
 _,err:=userDB.Exec(`CREATE TABLE IF NOT EXISTS public.whatsapp_pairing_sessions(session_key TEXT PRIMARY KEY,user_id UUID NOT NULL,phone_number TEXT NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),expires_at TIMESTAMPTZ NOT NULL); CREATE INDEX IF NOT EXISTS whatsapp_pair_user_idx ON public.whatsapp_pairing_sessions(user_id)`)
 return err
}

func newWhatsAppSessionKey() string { b:=make([]byte,16);if _,err:=rand.Read(b);err!=nil{return "wa-"+uuid.NewString()};return "wa-"+hex.EncodeToString(b) }

func userWhatsAppPairStartHandler(w http.ResponseWriter,r *http.Request){
 if r.Method!=http.MethodPost{userFeaturesJSON(w,http.StatusMethodNotAllowed,map[string]any{"status":"error","message":"POST required"});return}
 id,ok:=userDBID(r);if !ok{userFeaturesJSON(w,http.StatusUnauthorized,map[string]any{"status":"error","message":"Login required"});return}
 if err:=ensureUserWhatsAppPairingTables();err!=nil{userFeaturesJSON(w,500,map[string]any{"status":"error","message":"Could not initialize WhatsApp pairing"});return}
 var count int;if err:=userDB.QueryRow(`SELECT count(*) FROM public.user_whatsapp_accounts WHERE user_id=$1::uuid AND status<>'removed'`,id).Scan(&count);err!=nil{userFeaturesJSON(w,500,map[string]any{"status":"error"});return};if count>=3{userFeaturesJSON(w,409,map[string]any{"status":"error","message":"Maximum 3 WhatsApp accounts reached"});return}
 phone:=strings.TrimSpace(r.URL.Query().Get("phone"));phone=strings.NewReplacer("+",""," ","","-","").Replace(phone);if len(phone)<7||len(phone)>15{userFeaturesJSON(w,400,map[string]any{"status":"error","message":"Phone number must contain 7-15 digits"});return};for _,c:=range phone{if c<'0'||c>'9'{userFeaturesJSON(w,400,map[string]any{"status":"error","message":"Phone number must contain digits only"});return}}
 key:=newWhatsAppSessionKey();device:=waContainer.NewDevice();client:=whatsmeow.NewClient(device,waLog.Stdout("Client-"+key,"INFO",true));if err:=client.Connect();err!=nil{userFeaturesJSON(w,503,map[string]any{"status":"error","message":"Could not connect WhatsApp"});return};if !createPendingSession(key,&Session{client:client}){_=client.Disconnect();userFeaturesJSON(w,409,map[string]any{"status":"error","message":"Pairing session collision; try again"});return};expires:=time.Now().UTC().Add(160*time.Second);if _,err:=userDB.Exec(`INSERT INTO public.whatsapp_pairing_sessions(session_key,user_id,phone_number,expires_at) VALUES($1,$2::uuid,$3,$4)`,key,id,phone,expires);err!=nil{removeSession(key);_=client.Disconnect();userFeaturesJSON(w,500,map[string]any{"status":"error"});return};code,err:=client.PairPhone(context.Background(),phone,true,whatsmeow.PairClientChrome,"Chrome (Linux)");if err!=nil{removeSession(key);_=client.Disconnect();_=userDB.Exec(`DELETE FROM public.whatsapp_pairing_sessions WHERE session_key=$1`,key);userFeaturesJSON(w,502,map[string]any{"status":"error","message":err.Error()});return};userFeaturesJSON(w,200,map[string]any{"status":"success","session_key":key,"code":code,"expires_at":expires.Format(time.RFC3339Nano),"expires_in_seconds":160})
}

func userWhatsAppPairStatusHandler(w http.ResponseWriter,r *http.Request){
 if r.Method!=http.MethodGet{userFeaturesJSON(w,405,map[string]any{"status":"error"});return};id,ok:=userDBID(r);if !ok{userFeaturesJSON(w,401,map[string]any{"status":"error","message":"Login required"});return};key:=strings.TrimSpace(r.URL.Query().Get("session_key"));if key==""{userFeaturesJSON(w,400,map[string]any{"status":"error","message":"session_key is required"});return};if err:=ensureUserWhatsAppPairingTables();err!=nil{userFeaturesJSON(w,500,map[string]any{"status":"error"});return};var owner,phone string;var expires time.Time;if err:=userDB.QueryRow(`SELECT user_id::text,phone_number,expires_at FROM public.whatsapp_pairing_sessions WHERE session_key=$1`,key).Scan(&owner,&phone,&expires);err!=nil||owner!=id{userFeaturesJSON(w,404,map[string]any{"status":"error","message":"Pairing session not found"});return};s:=getSession(key);if s==nil||s.client==nil{if time.Now().UTC().After(expires){_=userDB.Exec(`DELETE FROM public.whatsapp_pairing_sessions WHERE session_key=$1`,key);userFeaturesJSON(w,410,map[string]any{"status":"error","message":"Pairing session expired"});return};userFeaturesJSON(w,200,map[string]any{"status":"success","state":"waiting","connected":false,"logged_in":false});return};logged:=s.client.IsLoggedIn();connected:=s.client.IsConnected();if !logged{if time.Now().UTC().After(expires){removeSession(key);_=userDB.Exec(`DELETE FROM public.whatsapp_pairing_sessions WHERE session_key=$1`,key);userFeaturesJSON(w,410,map[string]any{"status":"error","message":"Pairing session expired"});return};userFeaturesJSON(w,200,map[string]any{"status":"success","state":"waiting","connected":connected,"logged_in":false});return};if s.client.Store==nil||s.client.Store.ID==nil{userFeaturesJSON(w,500,map[string]any{"status":"error","message":"WhatsApp device ID unavailable"});return};jid:=*s.client.Store.ID;if _,err:=userDB.Exec(`INSERT INTO public.user_whatsapp_accounts(id,user_id,whatsapp_user_id,phone_number,status,linked_at,last_seen_at,current_send_total,today_send_total) VALUES($1,$2::uuid,$3,$4,'active',now(),now(),0,0)`,uuid.NewString(),id,key,jid.User,);err!=nil{if !strings.Contains(strings.ToLower(err.Error()),"duplicate") {userFeaturesJSON(w,500,map[string]any{"status":"error","message":"Could not save WhatsApp account"});return}};_=saveUserSession(key,jid);_=userDB.Exec(`DELETE FROM public.whatsapp_pairing_sessions WHERE session_key=$1`,key);setActive(key,s);userFeaturesJSON(w,200,map[string]any{"status":"success","state":"ready","connected":connected,"logged_in":true,"phone":jid.User,"account_key":key})
}

func init(){http.HandleFunc("/api/user/whatsapp/pair",userWhatsAppPairStartHandler);http.HandleFunc("/api/user/whatsapp/pair/status",userWhatsAppPairStatusHandler)}

var _ = types.DefaultUserServer
