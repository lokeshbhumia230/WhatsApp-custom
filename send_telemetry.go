package main

import (
    "fmt"
    "strings"
    "time"
)

// Send telemetry is diagnostic only. It does not change sending policy or bypass
// WhatsApp safety controls. It records where a send attempt stopped so India,
// Brazil, and other destinations can be compared with real data.
func ensureSendTelemetryTable() error {
    _, err := userDB.Exec(`CREATE TABLE IF NOT EXISTS public.send_telemetry (
        id BIGSERIAL PRIMARY KEY,
        user_id TEXT NOT NULL,
        target TEXT NOT NULL,
        country TEXT NOT NULL,
        stage TEXT NOT NULL,
        error TEXT,
        created_at TIMESTAMPTZ NOT NULL DEFAULT now()
    ); CREATE INDEX IF NOT EXISTS send_telemetry_country_stage_idx ON public.send_telemetry(country,stage,created_at); CREATE INDEX IF NOT EXISTS send_telemetry_user_idx ON public.send_telemetry(user_id,created_at)`)
    return err
}

func telemetryCountry(phone string) string {
    p := strings.TrimPrefix(strings.TrimSpace(phone), "+")
    switch {
    case strings.HasPrefix(p, "91"):
        return "IN"
    case strings.HasPrefix(p, "55"):
        return "BR"
    default:
        return "OTHER"
    }
}

func recordSendTelemetry(userID, target, stage string, sendErr error) {
    if userDB == nil || strings.TrimSpace(target) == "" { return }
    if err := ensureSendTelemetryTable(); err != nil {
        fmt.Printf("send telemetry table error: %v\n", err)
        return
    }
    var msg any
    if sendErr != nil { msg = sendErr.Error() }
    if _, err := userDB.Exec(`INSERT INTO public.send_telemetry(user_id,target,country,stage,error,created_at) VALUES($1,$2,$3,$4,$5,$6)`, userID, target, telemetryCountry(target), stage, msg, time.Now().UTC()); err != nil {
        fmt.Printf("send telemetry insert error: %v\n", err)
    }
}
