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

// baasStore remembers which BAAS user a Klei user id was minted for, in memory and, once a backing
// store is attached, durably. Writes that cannot reach the store (not attached yet, or the database
// is down) are kept pending, logged at error level, and written through the next time the store is
// reachable (flush, called on every set and by the reconnect loop).
type baasStore struct {
	mu      sync.Mutex
	by      map[string]string
	pending map[string]string
	back    kuBacking
}

// attach loads the persisted map, makes later sets write through to it, and flushes what was pending.
func (b *baasStore) attach(ctx context.Context, back kuBacking) error {
	m, err := back.load(ctx)
	if err != nil {
		return err
	}
	b.mu.Lock()
	if b.by == nil {
		b.by = map[string]string{}
	}
	for k, v := range m {
		if _, mine := b.pending[k]; !mine {
			b.by[k] = v
		}
	}
	b.back = back
	b.mu.Unlock()
	b.flush(ctx)
	return nil
}

func (b *baasStore) set(ku, baas string) {
	b.mu.Lock()
	if b.by == nil {
		b.by = map[string]string{}
	}
	if b.pending == nil {
		b.pending = map[string]string{}
	}
	b.by[ku] = baas
	b.pending[ku] = baas
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b.flush(ctx)
}

// flush writes the pending entries to the store, keeping any that fail.
func (b *baasStore) flush(ctx context.Context) {
	b.mu.Lock()
	back := b.back
	todo := make(map[string]string, len(b.pending))
	for k, v := range b.pending {
		todo[k] = v
	}
	b.mu.Unlock()
	if len(todo) == 0 {
		return
	}
	if back == nil {
		log.Printf("[klei] ERROR Klei-id map store not connected: %d entries held in memory only until it is", len(todo))
		return
	}
	for k, v := range todo {
		if err := back.upsert(ctx, k, v); err != nil {
			log.Printf("[klei] ERROR persisting the Klei-id map failed (%d entries pending, will retry): %v", len(todo), redact.Err(err))
			return
		}
		b.mu.Lock()
		if b.pending[k] == v {
			delete(b.pending, k)
		}
		b.mu.Unlock()
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

const sessionMetric = "game_sessions.dont-starve-together"

// runSessions saves the clock after every tick and starts the watcher.
func (s *server) runSessions(ctx context.Context, sess *emit.Sessions, every time.Duration) {
	sess.SetOnTick(s.clock.save)
	sess.Watch(ctx, every, func() []emit.Room { return s.rewardRooms(time.Now()) })
}
