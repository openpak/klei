package main

// Rewards sessions for Don't Starve Together.
//
// What the lobby can see: a client-hosted DST world broadcasts its listing and keeps it alive,
// and the listing's `players` field is the host's own roster, a Lua table string that names each
// connected player by Klei user id (`netid="KU_xxxxxxxx"`). Joiners and the host talk UDP to each
// other, so that roster is the only membership the server ever observes. A player counts once per
// room after REWARDS_SESSION_MIN (3 minutes) of unbroken time in a live listing together with at
// least one other player this host can map to an OpenPak account.
//
// Mapping: kuid is a one-way hash of the BAAS user id, so LoginViaSwitch remembers KU -> BAAS id
// in Postgres (kustore.go) when KLEI_DATABASE_URL is set, loaded at start. Unmapped players are left
// out (and so do not count as the second player). Nothing is guessed: a roster entry that is not a
// KU id kleid minted for a logged-in console is ignored.
//
// The room is the listing row plus the time its world was written, so a host that restarts its
// world (same guid) starts a new room and a new dedupe key.

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"sync"
	"time"

	emit "github.com/openpak/rewards-emit"

	"openpak/klei/redact"
)

// kuBacking is where the KU -> BAAS map survives a restart (Postgres in production).
type kuBacking interface {
	upsert(ctx context.Context, ku, baas string) error
	load(ctx context.Context) (map[string]string, error)
}

// baasStore remembers which BAAS user a Klei user id was minted for, in memory and, when a
// backing store is attached, durably.
type baasStore struct {
	mu   sync.Mutex
	by   map[string]string
	back kuBacking
}

// attach loads the persisted map and makes later sets write through to it.
func (b *baasStore) attach(ctx context.Context, back kuBacking) error {
	m, err := back.load(ctx)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.by == nil {
		b.by = map[string]string{}
	}
	for k, v := range m {
		b.by[k] = v
	}
	b.back = back
	return nil
}

func (b *baasStore) set(ku, baas string) {
	b.mu.Lock()
	if b.by == nil {
		b.by = map[string]string{}
	}
	b.by[ku] = baas
	back := b.back
	b.mu.Unlock()
	if back != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := back.upsert(ctx, ku, baas); err != nil {
			log.Printf("[klei] persisting the Klei-id map failed: %v", redact.Err(err))
		}
	}
}

func (b *baasStore) get(ku string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.by[ku]
}

// rosterIDs matches the Klei user ids in a listing's `players` Lua string.
var rosterIDs = regexp.MustCompile(`(?:netid|userid)\s*=\s*["'](KU_[A-Za-z0-9_-]{8})["']`)

func rosterKUs(players string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range rosterIDs.FindAllStringSubmatch(players, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// rewardRooms is the live listings as rewards rooms.
func (s *server) rewardRooms(now time.Time) []emit.Room {
	s.lobbies.mu.Lock()
	defer s.lobbies.mu.Unlock()
	var rooms []emit.Room
	for id, l := range s.lobbies.rows {
		if !l.written || now.Sub(l.seen) > listingTTL {
			continue
		}
		room := emit.Room{ID: fmt.Sprintf("dst:%s@%d", id, l.created.Unix())}
		players, _ := l.data["players"].(string)
		for _, ku := range rosterKUs(players) {
			if baas := s.baas.get(ku); baas != "" {
				room.Players = append(room.Players, emit.Identity{Namespace: "switch", Subject: baas})
			}
		}
		rooms = append(rooms, room)
	}
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].ID < rooms[j].ID })
	return rooms
}

func (s *server) watchSessions(ctx context.Context, rw *emit.Client, every time.Duration) {
	if !rw.Enabled() {
		return
	}
	sess := emit.NewSessions(rw, "game_sessions.dont-starve-together", 0)
	sess.Watch(ctx, every, func() []emit.Room { return s.rewardRooms(time.Now()) })
}
