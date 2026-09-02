package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

var whatsmeowTables = []string{
	"whatsmeow_device", "whatsmeow_identity_keys", "whatsmeow_pre_keys", "whatsmeow_sessions",
	"whatsmeow_sender_keys", "whatsmeow_app_state_sync_keys", "whatsmeow_app_state_version",
	"whatsmeow_app_state_mutation_macs", "whatsmeow_contacts", "whatsmeow_chat_settings",
	"whatsmeow_message_secrets", "whatsmeow_privacy_tokens", "whatsmeow_nct_salt", "whatsmeow_lid_map",
	"whatsmeow_event_buffer", "whatsmeow_retry_buffer",
}

func migrateLegacySQLite(ctx context.Context) error {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("SUPABASE_MIGRATE_SQLITE")), "false") { return nil }
	var deviceCount int
	if err := userDB.QueryRowContext(ctx, `SELECT count(*) FROM whatsmeow_device`).Scan(&deviceCount); err != nil { return err }
	if deviceCount == 0 {
		if _, err := os.Stat("store.db"); err == nil {
			if err := copySQLiteTables(ctx, "store.db", whatsmeowTables); err != nil { return fmt.Errorf("migrate whatsmeow SQLite store: %w", err) }
		}
	}
	if _, err := os.Stat("users.db"); err == nil { if err := copyLegacyUserSessions(ctx,"users.db"); err != nil { return fmt.Errorf("migrate user sessions: %w",err) } }
	return nil
}

func copyLegacyUserSessions(ctx context.Context,path string) error {
	src,err:=sql.Open("sqlite3","file:"+path+"?mode=ro");if err!=nil{return err};defer src.Close()
	rows,err:=src.QueryContext(ctx,`SELECT user_id,jid,updated_at FROM user_sessions`);if err!=nil{return nil};defer rows.Close()
	for rows.Next(){var uid,jid string;var updated any;if err:=rows.Scan(&uid,&jid,&updated);err!=nil{return err};if _,err=userDB.ExecContext(ctx,`INSERT INTO user_sessions(user_id,jid,updated_at) VALUES($1,$2,COALESCE($3::timestamptz,now())) ON CONFLICT(user_id) DO UPDATE SET jid=excluded.jid,updated_at=excluded.updated_at`,uid,jid,updated);err!=nil{return err}}
	return rows.Err()
}

func copySQLiteTables(ctx context.Context,path string,tables []string) error {
	src,err:=sql.Open("sqlite3","file:"+path+"?mode=ro");if err!=nil{return err};defer src.Close()
	for _,table:=range tables{if err:=copySQLiteTable(ctx,src,table);err!=nil{return fmt.Errorf("%s: %w",table,err)}}
	return nil
}

func copySQLiteTable(ctx context.Context,src *sql.DB,table string) error {
	cols,types,err:=commonColumns(ctx,src,table);if err!=nil{return err};if len(cols)==0{return nil}
	quoted:=make([]string,len(cols));for i,c:=range cols{quoted[i]=`"`+strings.ReplaceAll(c,`"`,`""`)+`"`}
	rows,err:=src.QueryContext(ctx,`SELECT `+strings.Join(quoted,",")+` FROM "`+strings.ReplaceAll(table,`"`,`""`)+`"`);if err!=nil{return nil};defer rows.Close()
	placeholders:=make([]string,len(cols));for i:=range cols{placeholders[i]=fmt.Sprintf("$%d",i+1)}
	q:=`INSERT INTO "`+table+`" (`+strings.Join(quoted,",")+`) VALUES (`+strings.Join(placeholders,",")+`) ON CONFLICT DO NOTHING`
	for rows.Next(){values:=make([]any,len(cols));ptrs:=make([]any,len(cols));for i:=range values{ptrs[i]=&values[i]};if err:=rows.Scan(ptrs...);err!=nil{return err};for i:=range values{values[i]=normalizeSQLiteValue(values[i],types[cols[i]])};if _,err:=userDB.ExecContext(ctx,q,values...);err!=nil{return err}}
	return rows.Err()
}

func commonColumns(ctx context.Context,src *sql.DB,table string)([]string,map[string]string,error){
	srcRows,err:=src.QueryContext(ctx,`PRAGMA table_info("`+strings.ReplaceAll(table,`"`,`""`)+`")`);if err!=nil{return nil,nil,err};defer srcRows.Close();var sourceCols []string
	for srcRows.Next(){var cid int;var name,typ string;var notnull,pk int;var dflt any;if err:=srcRows.Scan(&cid,&name,&typ,&notnull,&dflt,&pk);err!=nil{return nil,nil,err};sourceCols=append(sourceCols,name)}
	targetRows,err:=userDB.QueryContext(ctx,`SELECT column_name,data_type FROM information_schema.columns WHERE table_schema='public' AND table_name=$1`,table);if err!=nil{return nil,nil,err};defer targetRows.Close();targetTypes:=map[string]string{}
	for targetRows.Next(){var n,t string;if err:=targetRows.Scan(&n,&t);err!=nil{return nil,nil,err};targetTypes[n]=t}
	var cols []string;types:=map[string]string{};for _,c:=range sourceCols{if t,ok:=targetTypes[c];ok{cols=append(cols,c);types[c]=t}};return cols,types,nil
}

func normalizeSQLiteValue(v any,pgType string)any{if v==nil{return nil};switch pgType{case "boolean":switch x:=v.(type){case int64:return x!=0;case int:return x!=0;case []byte:return strings.EqualFold(string(x),"true")||string(x)=="1"};case "uuid":if x,ok:=v.([]byte);ok{if len(x)==16{return uuid.UUID(x).String()};return string(x)}};return v}
