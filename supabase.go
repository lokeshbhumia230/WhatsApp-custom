package main

import (
    "context"
    "fmt"
    "os"
    "strings"

    "github.com/jackc/pgx/v5/pgxpool"
)

var supabaseDB *pgxpool.Pool

func initSupabase(ctx context.Context) error {
    dsn := strings.TrimSpace(os.Getenv("SUPABASE_DB_URL"))
    if dsn == "" {
        return fmt.Errorf("SUPABASE_DB_URL is not configured")
    }

    cfg, err := pgxpool.ParseConfig(dsn)
    if err != nil {
        return fmt.Errorf("parse Supabase database URL: %w", err)
    }
    cfg.MaxConns = 10
    cfg.MinConns = 1

    pool, err := pgxpool.NewWithConfig(ctx, cfg)
    if err != nil {
        return fmt.Errorf("create Supabase connection pool: %w", err)
    }
    if err := pool.Ping(ctx); err != nil {
        pool.Close()
        return fmt.Errorf("ping Supabase: %w", err)
    }

    supabaseDB = pool
    return nil
}

func closeSupabase() {
    if supabaseDB != nil {
        supabaseDB.Close()
    }
}
