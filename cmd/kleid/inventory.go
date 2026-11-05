package main

// The player's DST cosmetics, from Rewards (card openpak/prds#141). Rewards owns who has what (a points purchase, a mission,
// an admin gift); kleid only asks. The player is named by network identity (namespace switch, subject = the BAAS user id the
// Klei id was minted for), so no account id is stored here. Answers are cached for a minute per player and dropped at once when
// the account core announces `entitlement_changed` for the game; a Rewards outage serves the last answer for a day, then nothing.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	accountv1 "github.com/openpak/account-api/openpak/account/v1"
)

const rewardsGame = "dst"

type invEntry struct {
	account string
	types   []string
	fresh   time.Time // the answer is current until this
	stale   time.Time // and usable if Rewards cannot be reached until this
}

type rewardsInventory struct {
	base, key string
	http      *http.Client
	now       func() time.Time
	mu        sync.Mutex
	by        map[string]invEntry // Klei user id -> answer
}

// newRewardsInventory reads REWARDS_EVENTS_URL and REWARDS_EVENTS_KEY (the same pair the reward-event emitter uses).
func newRewardsInventory() *rewardsInventory {
	base, key := strings.TrimRight(os.Getenv("REWARDS_EVENTS_URL"), "/"), os.Getenv("REWARDS_EVENTS_KEY")
	if key == "" {
		if f := os.Getenv("REWARDS_EVENTS_KEY_FILE"); f != "" {
			if b, err := os.ReadFile(f); err == nil {
				key = strings.TrimSpace(string(b))
			}
		}
	}
	if base == "" || key == "" {
		return nil
	}
	return &rewardsInventory{base: base, key: key, http: &http.Client{Timeout: 4 * time.Second}, now: time.Now, by: map[string]invEntry{}}
}

// owned returns the DST item keys the player owns, deliverable ones only (Rewards filters).
func (r *rewardsInventory) owned(ctx context.Context, ku, baas string) []string {
	r.mu.Lock()
	e, ok := r.by[ku]
	r.mu.Unlock()
	if ok && r.now().Before(e.fresh) {
		return e.types
	}
	if baas == "" {
		return nil
	}
	types, account, err := r.fetch(ctx, baas)
	if err != nil {
		log.Printf("[klei] rewards inventory: %v", err)
		if ok && r.now().Before(e.stale) {
			return e.types
		}
		return nil
	}
	r.mu.Lock()
	r.by[ku] = invEntry{account: account, types: types, fresh: r.now().Add(time.Minute), stale: r.now().Add(24 * time.Hour)}
	r.mu.Unlock()
	return types
}

func (r *rewardsInventory) fetch(ctx context.Context, baas string) (types []string, account string, err error) {
	q := url.Values{"namespace": {"switch"}, "subject": {baas}, "game": {rewardsGame}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, r.base+"/internal/v1/adapter/inventory?"+q.Encode(), nil)
	req.Header.Set("X-Internal-Key", r.key)
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("rewards unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		return nil, "", fmt.Errorf("rewards answered %d", resp.StatusCode)
	}
	var inv struct {
		Account string `json:"account"`
		Owned   []struct {
			GameRef string `json:"game_ref"`
		} `json:"owned"`
		Base []struct {
			GameRef string `json:"game_ref"`
		} `json:"base"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&inv); err != nil {
		return nil, "", fmt.Errorf("rewards reply: %w", err)
	}
	seen := map[string]bool{}
	for _, o := range inv.Owned {
		if o.GameRef != "" && !seen[o.GameRef] {
			seen[o.GameRef] = true
			types = append(types, o.GameRef)
		}
	}
	for _, b := range inv.Base {
		if b.GameRef != "" && !seen[b.GameRef] {
			seen[b.GameRef] = true
			types = append(types, b.GameRef)
		}
	}
	return types, inv.Account, nil
}

// drop forgets the players of an account: the next GetAllItems asks Rewards again.
func (r *rewardsInventory) drop(account string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for ku, e := range r.by {
		if e.account == account {
			e.fresh = time.Time{} // keep the stale copy for an outage
			r.by[ku] = e
			n++
		}
	}
	return n
}

// follow drops cached inventories on `entitlement_changed` for this game until ctx ends. It starts at the stream's current
// position (a restart has an empty cache anyway) and reconnects with the last version it saw.
func (r *rewardsInventory) follow(ctx context.Context) {
	addr, key := os.Getenv("ACCOUNT_GRPC_ADDR"), os.Getenv("ACCOUNT_INTERNAL_KEY")
	if addr == "" || key == "" {
		log.Printf("[klei] no account core: inventories refresh every minute only")
		return
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("[klei] ERROR account events: %v", err)
		return
	}
	defer conn.Close()
	ev := accountv1.NewEventsClient(conn)
	authed := func(c context.Context) context.Context {
		return metadata.AppendToOutgoingContext(c, "x-internal-key", key)
	}
	var since uint64
	started := false
	for ctx.Err() == nil {
		if !started {
			pc, cancel := context.WithTimeout(authed(ctx), 5*time.Second)
			resp, err := ev.PollEvents(pc, &accountv1.PollEventsRequest{SinceVersion: 1 << 62, Limit: 1})
			cancel()
			if err == nil {
				since, started = resp.GetMaxVersion(), true
			}
		}
		if started {
			stream, err := ev.SubscribeAccountEvents(authed(ctx), &accountv1.SubscribeAccountEventsRequest{SinceVersion: since})
			for err == nil {
				var e *accountv1.InvalidationEvent
				if e, err = stream.Recv(); err != nil {
					break
				}
				since = e.GetVersion()
				if e.GetType() == "entitlement_changed" && e.GetSubjectId() == rewardsGame {
					if n := r.drop(e.GetAccountId()); n > 0 {
						log.Printf("[klei] inventory refreshed for %d player(s) after entitlement_changed", n)
					}
				}
			}
			if ctx.Err() == nil {
				log.Printf("[klei] account events: %v (reconnecting)", err)
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
	}
}
