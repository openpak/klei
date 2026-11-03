package main

// The KU -> BAAS user id map in Postgres, so a restart does not forget who is who.
// One small table; the pool is capped at two connections.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type pgKU struct{ pool *pgxpool.Pool }

const kuSchema = `CREATE TABLE IF NOT EXISTS klei_users (
	ku           text PRIMARY KEY,
	baas_user_id text NOT NULL,
	last_seen    timestamptz NOT NULL DEFAULT now()
)`

func openKU(ctx context.Context, url string) (*pgKU, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, kuSchema); err != nil {
		pool.Close()
		return nil, err
	}
	return &pgKU{pool}, nil
}

func (p *pgKU) upsert(ctx context.Context, ku, baas string) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO klei_users (ku, baas_user_id, last_seen) VALUES ($1,$2,now())
		ON CONFLICT (ku) DO UPDATE SET baas_user_id = EXCLUDED.baas_user_id, last_seen = now()`, ku, baas)
	return err
}

func (p *pgKU) load(ctx context.Context) (map[string]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT ku, baas_user_id FROM klei_users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var ku, baas string
		if err := rows.Scan(&ku, &baas); err != nil {
			return nil, err
		}
		out[ku] = baas
	}
	return out, rows.Err()
}

// attachKUStore wires the database in when KLEI_DATABASE_URL is set. Without it, or when the
// database is unreachable, kleid runs with the in-memory map: logins and lobbies never depend on it.
func attachKUStore(ctx context.Context, s *server, url string, logf func(string, ...any)) {
	if url == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	db, err := openKU(cctx, url)
	if err == nil {
		err = s.baas.attach(cctx, db)
	}
	if err != nil {
		logf("[klei] Klei-id map store unavailable, using memory only: %v", err)
		return
	}
	logf("[klei] Klei-id map persisted in Postgres")
}
