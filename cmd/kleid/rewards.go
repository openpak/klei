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
// in memory. A kleid restart forgets it until the player logs in again; unmapped players are left
// out (and so do not count as the second player). Nothing is guessed: a roster entry that is not a
// KU id kleid minted for a logged-in console is ignored.
//
// The room is the listing row plus the time its world was written, so a host that restarts its
// world (same guid) starts a new room and a new dedupe key.

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"

	emit "github.com/openpak/rewards-emit"
)

// baasStore remembers which BAAS user a Klei user id was minted for.
type baasStore struct {
	mu sync.Mutex
	by map[string]string
}

func (b *baasStore) set(ku, baas string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.by == nil {
		b.by = map[string]string{}
	}
	b.by[ku] = baas
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
