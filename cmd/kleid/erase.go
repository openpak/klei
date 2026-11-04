package main

// Account erasure (card openpak/prds#120). kleid keys everything by the Klei user id (KU_..., a one-way
// hash of the BAAS user id), so the account's BAAS ids come from the core's Switch links. Erased:
// the KU -> BAAS map (klei_users), the account's entries in the rewards session clock
// (klei_session_clock), and the in-memory name, version, id-map and lobby listings that mention the KU.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	emit "github.com/openpak/rewards-emit"
)

// eraseBacking is the Postgres side of an erasure.
type eraseBacking interface {
	eraseBaas(ctx context.Context, baas []string) (int, error)
}

func (p *pgKU) eraseBaas(ctx context.Context, baas []string) (int, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	t, err := tx.Exec(ctx, `DELETE FROM klei_users WHERE baas_user_id = ANY($1)`, baas)
	if err != nil {
		return 0, err
	}
	n := int(t.RowsAffected())
	var raw string
	err = tx.QueryRow(ctx, `SELECT state FROM klei_session_clock WHERE metric = $1 FOR UPDATE`, sessionMetric).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	if err == nil {
		out, removed, ok := scrubClock([]byte(raw), baas)
		if ok && removed > 0 {
			if _, err := tx.Exec(ctx, `UPDATE klei_session_clock SET state = $2, saved_at = now() WHERE metric = $1`, sessionMetric, string(out)); err != nil {
				return 0, err
			}
			n += removed
		}
	}
	return n, tx.Commit(ctx)
}

// scrubClock drops the clock keys (room NUL namespace NUL subject) that name one of the BAAS ids.
func scrubClock(raw []byte, baas []string) (out []byte, removed int, ok bool) {
	var st emit.State
	if json.Unmarshal(raw, &st) != nil {
		return nil, 0, false
	}
	mine := func(k string) bool {
		for _, b := range baas {
			if strings.HasSuffix(k, "\x00switch\x00"+b) {
				return true
			}
		}
		return false
	}
	for k := range st.Since {
		if mine(k) {
			delete(st.Since, k)
			removed++
		}
	}
	kept := st.Emitted[:0:0]
	for _, k := range st.Emitted {
		if mine(k) {
			removed++
		} else {
			kept = append(kept, k)
		}
	}
	st.Emitted = kept
	out, err := json.Marshal(st)
	return out, removed, err == nil
}

// Erase removes everything kleid holds for the core account; a second run removes nothing.
func (s *server) Erase(ctx context.Context, account string) (int, error) {
	if s.links == nil {
		return 0, errNoCore
	}
	baas, err := s.links.SwitchSubjects(ctx, account)
	if err != nil {
		return 0, err
	}
	if len(baas) == 0 {
		return 0, nil
	}
	n := 0
	kus := map[string]bool{}
	for _, b := range baas {
		kus[kuid(b)] = true
	}
	// Memory first, so the next rewards tick no longer sees the player.
	s.baas.mu.Lock()
	for ku := range s.baas.by {
		if kus[ku] {
			delete(s.baas.by, ku)
			delete(s.baas.pending, ku)
		}
	}
	s.baas.mu.Unlock()
	for ku := range kus {
		s.names.mu.Lock()
		delete(s.names.by, ku)
		s.names.mu.Unlock()
		s.versions.mu.Lock()
		delete(s.versions.by, ku)
		s.versions.mu.Unlock()
	}
	s.lobbies.mu.Lock()
	for id, l := range s.lobbies.rows {
		raw, _ := json.Marshal(l.data)
		for ku := range kus {
			if strings.Contains(string(raw), ku) {
				delete(s.lobbies.rows, id)
				break
			}
		}
	}
	s.lobbies.mu.Unlock()

	if e := s.eraser; e != nil {
		c, err := e.eraseBaas(ctx, baas)
		if err != nil {
			return n, err
		}
		n += c
	}
	s.clock.mu.Lock()
	s.clock.last = nil
	s.clock.mu.Unlock()
	return n, nil
}
