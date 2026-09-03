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
	"go.mau.fi/whatsmeow/types"
)

// Account health is deliberately enforcement-aware: when WhatsApp reports a
// reachout timelock or a new-chat cap, the sender is paused instead of retrying
// the same operation and generating more enforcement traffic.
type accountHealthState struct {
	UserID             string
	Status             string
	TimelockActive     bool
	TimelockType       string
	TimelockUntil      time.Time
	CapStatus          string
	CapTotal           int
	CapUsed            int
	CapRemaining       int
	CapCheckedAt       time.Time
	LastCheckedAt      time.Time
	LastError          string
	UpdatedAt          time.Time
}

var accountHealthMu sync.RWMutex
var accountHealthCache = map[string]accountHealthState{}
var accountHealthFetchMu sync.Mutex

func ensureAccountHealthTable() error {
	_, err := userDB.Exec(`CREATE TABLE IF NOT EXISTS public.whatsapp_account_health (
		user_id TEXT PRIMARY KEY,
		status TEXT NOT NULL DEFAULT 'healthy',
		timelock_active BOOLEAN NOT NULL DEFAULT FALSE,
		timelock_type TEXT,
		timelock_until TIMESTAMPTZ,
		cap_status TEXT,
		cap_total INTEGER NOT NULL DEFAULT 0,
		cap_used INTEGER NOT NULL DEFAULT 0,
		cap_remaining INTEGER NOT NULL DEFAULT 0,
		cap_checked_at TIMESTAMPTZ,
		last_checked_at TIMESTAMPTZ,
		last_error TEXT,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);`)
	return err
}

func persistAccountHealth(s accountHealthState) {
	if userDB == nil { return }
	if err := ensureAccountHealthTable(); err != nil { fmt.Printf("account health table error: %v\n", err); return }
	_, err := userDB.Exec(`INSERT INTO public.whatsapp_account_health
		(user_id,status,timelock_active,timelock_type,timelock_until,cap_status,cap_total,cap_used,cap_remaining,cap_checked_at,last_checked_at,last_error,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NOW())
		ON CONFLICT(user_id) DO UPDATE SET status=EXCLUDED.status,timelock_active=EXCLUDED.timelock_active,
		timelock_type=EXCLUDED.timelock_type,timelock_until=EXCLUDED.timelock_until,cap_status=EXCLUDED.cap_status,
		cap_total=EXCLUDED.cap_total,cap_used=EXCLUDED.cap_used,cap_remaining=EXCLUDED.cap_remaining,
		cap_checked_at=EXCLUDED.cap_checked_at,last_checked_at=EXCLUDED.last_checked_at,last_error=EXCLUDED.last_error,updated_at=NOW()`,
		s.UserID,s.Status,s.TimelockActive,s.TimelockType,nullTime(s.TimelockUntil),s.CapStatus,s.CapTotal,s.CapUsed,s.CapRemaining,nullTime(s.CapCheckedAt),nullTime(s.LastCheckedAt),s.LastError)
	if err != nil { fmt.Printf("account health persist failed for %s: %v\n", s.UserID, err) }
}

func nullTime(t time.Time) any { if t.IsZero() { return nil }; return t }

func setAccountHealth(s accountHealthState) {
	accountHealthMu.Lock(); accountHealthCache[s.UserID] = s; accountHealthMu.Unlock()
	persistAccountHealth(s)
}

func getAccountHealth(userID string) accountHealthState {
	accountHealthMu.RLock(); s, ok := accountHealthCache[userID]; accountHealthMu.RUnlock()
	if ok { return s }
	if userDB == nil { return accountHealthState{UserID:userID,Status:"unknown"} }
	if err := ensureAccountHealthTable(); err != nil { return accountHealthState{UserID:userID,Status:"unknown",LastError:err.Error()} }
	var st accountHealthState
	var tu,cc,lc,ua sql.NullTime
	err := userDB.QueryRow(`SELECT user_id,status,timelock_active,COALESCE(timelock_type,''),timelock_until,COALESCE(cap_status,''),cap_total,cap_used,cap_remaining,cap_checked_at,last_checked_at,COALESCE(last_error,''),updated_at FROM public.whatsapp_account_health WHERE user_id=$1`,userID).
		Scan(&st.UserID,&st.Status,&st.TimelockActive,&st.TimelockType,&tu,&st.CapStatus,&st.CapTotal,&st.CapUsed,&st.CapRemaining,&cc,&lc,&st.LastError,&ua)
	if err != nil { return accountHealthState{UserID:userID,Status:"unknown"} }
	if tu.Valid { st.TimelockUntil=tu.Time }; if cc.Valid { st.CapCheckedAt=cc.Time }; if lc.Valid { st.LastCheckedAt=lc.Time }; if ua.Valid { st.UpdatedAt=ua.Time }
	accountHealthMu.Lock(); accountHealthCache[userID]=st; accountHealthMu.Unlock()
	return st
}

func is463Error(err error) bool {
	if err == nil { return false }
	s := strings.ToLower(err.Error())
	return strings.Contains(s,"server returned error 463") || strings.Contains(s,"error 463")
}

func parseUnixString(v any) time.Time {
	s := strings.TrimSpace(fmt.Sprint(v)); if s == "" || s == "<nil>" { return time.Time{} }
	var n int64
	if _,err:=fmt.Sscan(s,&n); err==nil && n>0 { return time.Unix(n,0) }
	if t,err:=time.Parse(time.RFC3339,s); err==nil { return t }
	return time.Time{}
}

func reachoutExpiry(state *types.AccountReachoutTimelock) time.Time {
	if state == nil { return time.Time{} }
	b,_:=json.Marshal(state)
	var m map[string]any
	if json.Unmarshal(b,&m)!=nil { return time.Time{} }
	return parseUnixString(m["time_enforcement_ends"])
}

func accountHealthSnapshot(userID string, client *whatsmeow.Client, force bool) accountHealthState {
	now:=time.Now().UTC()
	current:=getAccountHealth(userID)
	if !force && current.TimelockActive && (current.TimelockUntil.IsZero() || now.Before(current.TimelockUntil)) { return current }
	if !force && !current.CapCheckedAt.IsZero() && now.Sub(current.CapCheckedAt)<60*time.Second { return current }
	if client==nil || !client.IsLoggedIn() || !client.IsConnected() { return current }

	accountHealthFetchMu.Lock(); defer accountHealthFetchMu.Unlock()
	current=getAccountHealth(userID)
	if !force && current.TimelockActive && (current.TimelockUntil.IsZero() || now.Before(current.TimelockUntil)) { return current }
	ctx,cancel:=context.WithTimeout(context.Background(),12*time.Second); defer cancel()
	updated:=current; updated.UserID=userID; updated.LastCheckedAt=now; updated.LastError=""

	if tl,err:=client.GetAccountReachoutTimelock(ctx); err==nil && tl!=nil {
		updated.TimelockActive=tl.IsActive
		updated.TimelockType=string(tl.EnforcementType)
		updated.TimelockUntil=reachoutExpiry(tl)
		if tl.IsActive { updated.Status="timelocked" } else if updated.Status=="timelocked" { updated.Status="healthy" }
	} else if err!=nil { updated.LastError="timelock check: "+err.Error() }

	if cap,err:=client.GetNewChatMessageCappingInfo(ctx); err==nil && cap!=nil {
		updated.CapStatus=string(cap.CappingStatus); updated.CapTotal=cap.TotalQuota; updated.CapUsed=cap.UsedQuota; updated.CapRemaining=cap.TotalQuota-cap.UsedQuota; if updated.CapRemaining<0 { updated.CapRemaining=0 }; updated.CapCheckedAt=now
		if cap.CappingStatus==types.NewChatMessageCappingStatusCapped { updated.Status="capped" }
	} else if err!=nil && updated.LastError=="" { updated.LastError="message cap check: "+err.Error() }
	if updated.Status=="" { updated.Status="healthy" }
	setAccountHealth(updated)
	return updated
}

// checkAccountHealthForSend is fail-open only for an unavailable health query;
// a server-confirmed restriction is always fail-closed.
func checkAccountHealthForSend(userID string, client *whatsmeow.Client) error {
	s:=accountHealthSnapshot(userID,client,false)
	now:=time.Now().UTC()
	if s.TimelockActive && (s.TimelockUntil.IsZero() || now.Before(s.TimelockUntil)) { return fmt.Errorf("WhatsApp reachout timelock is active%s", healthUntilText(s.TimelockType,s.TimelockUntil)) }
	if s.CapStatus==string(types.NewChatMessageCappingStatusCapped) { return fmt.Errorf("WhatsApp new-chat message cap is active; sending is paused until WhatsApp reports the cap is cleared") }
	return nil
}

func healthUntilText(kind string, until time.Time) string { parts:=""; if kind!="" { parts=" (type="+kind }; if !until.IsZero() { if parts=="" { parts=" (" } else { parts+=", " }; parts+="until="+until.Format(time.RFC3339) }; if parts!="" { parts+=")" }; return parts }

func markAccountTimelock(userID string, client *whatsmeow.Client, err error) {
	now:=time.Now().UTC(); s:=getAccountHealth(userID); s.UserID=userID; s.Status="timelocked"; s.TimelockActive=true; s.LastCheckedAt=now; s.UpdatedAt=now; s.LastError=err.Error()
	if tl,fetchErr:=client.GetAccountReachoutTimelock(context.Background()); fetchErr==nil && tl!=nil { s.TimelockActive=tl.IsActive; s.TimelockType=string(tl.EnforcementType); s.TimelockUntil=reachoutExpiry(tl); if !tl.IsActive { s.Status="healthy" } }
	setAccountHealth(s)
}

func adminAccountHealthDataHandler(w http.ResponseWriter,r *http.Request){
	enableCORS(w); w.Header().Set("Content-Type","application/json"); if _,ok:=requireAdmin(w,r); !ok{return}
	uid:=strings.TrimSpace(r.URL.Query().Get("user_id")); if uid=="" { http.Error(w,"user_id is required",400); return }
	s:=getAccountHealth(uid); _=json.NewEncoder(w).Encode(s)
}

func adminAccountHealthRefreshHandler(w http.ResponseWriter,r *http.Request){
	enableCORS(w); w.Header().Set("Content-Type","application/json"); if _,ok:=requireAdmin(w,r); !ok{return}
	uid:=strings.TrimSpace(r.URL.Query().Get("user_id")); s:=getSession(uid); if uid==""||s==nil||s.client==nil { http.Error(w,"connected WhatsApp account not found",404); return }
	state:=accountHealthSnapshot(uid,s.client,true); _=json.NewEncoder(w).Encode(state)
}

func init(){ http.HandleFunc("/admin/account-health/data",adminAccountHealthDataHandler); http.HandleFunc("/admin/account-health/refresh",adminAccountHealthRefreshHandler) }
