package main

import (
 "database/sql"
 "sync"
 "go.mau.fi/whatsmeow"
 "go.mau.fi/whatsmeow/store/sqlstore"
)

type Session struct {
 client *whatsmeow.Client
 mu sync.Mutex
}

type SessionManager struct {
 mu sync.RWMutex
 sessions map[string]*Session
 pending map[string]*Session
}

var manager = SessionManager{
 sessions: make(map[string]*Session),
 pending: make(map[string]*Session),
}

var userDB *sql.DB
var waContainer *sqlstore.Container
