package main

// The player's DST cosmetics, from Rewards (card openpak/prds#141). Rewards owns who has what (a points purchase, a mission,
// an admin gift); kleid only asks. The player is named by network identity (namespace switch, subject = the BAAS user id the
// Klei id was minted for), so no account id is stored here. Answers are cached for a minute per player and dropped at once when
// the account core announces `entitlement_changed` for the game; a Rewards outage serves the last answer for a day, then nothing.

import (
	"bytes"
	"context"
	_ "embed"
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

//go:embed packs.json
var packsJSON []byte

// packContents is what buying each Shop pack gives (tools/catalog/gen_catalog.py, from the game's misc_items.lua).
var packContents = func() map[string][]string {
	m := map[string][]string{}
	if err := json.Unmarshal(packsJSON, &m); err != nil {
		panic("packs.json: " + err.Error())
	}
	return m
}()

// expand turns owned Rewards items into the item keys the game lists: a pack is not kept, its contents are.
func expand(refs []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(k string) {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, r := range refs {
		if items, ok := packContents[r]; ok {
			for _, i := range items {
				add(i)
			}
			continue
		}
		add(r)
	}
	return out
}

type invEntry struct {
	balance int64
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
	shopItems []ShopItem
	shopUntil time.Time
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

// owned returns the DST item keys the player owns (deliverable ones only; Rewards filters, packs are expanded) and the player's
// points, which the game shows as its currency.
func (r *rewardsInventory) owned(ctx context.Context, ku, baas string) ([]string, int64) {
	r.mu.Lock()
	e, ok := r.by[ku]
	r.mu.Unlock()
	if ok && r.now().Before(e.fresh) {
		return e.types, e.balance
	}
	if baas == "" {
		return nil, 0
	}
	refs, account, balance, err := r.fetch(ctx, baas)
	if err != nil {
		log.Printf("[klei] rewards inventory: %v", err)
		if ok && r.now().Before(e.stale) {
			return e.types, e.balance
		}
		return nil, 0
	}
	types := expand(refs)
	r.mu.Lock()
	r.by[ku] = invEntry{account: account, balance: balance, types: types, fresh: r.now().Add(time.Minute), stale: r.now().Add(24 * time.Hour)}
	r.mu.Unlock()
	return types, balance
}

func (r *rewardsInventory) fetch(ctx context.Context, baas string) (types []string, account string, balance int64, err error) {
	q := url.Values{"namespace": {"switch"}, "subject": {baas}, "game": {rewardsGame}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, r.base+"/internal/v1/adapter/inventory?"+q.Encode(), nil)
	req.Header.Set("X-Internal-Key", r.key)
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, "", 0, fmt.Errorf("rewards unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		return nil, "", 0, fmt.Errorf("rewards answered %d", resp.StatusCode)
	}
	var inv struct {
		Account string `json:"account"`
		Balance int64  `json:"balance"`
		Owned   []struct {
			GameRef string `json:"game_ref"`
		} `json:"owned"`
		Base []struct {
			GameRef string `json:"game_ref"`
		} `json:"base"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&inv); err != nil {
		return nil, "", 0, fmt.Errorf("rewards reply: %w", err)
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
	return types, inv.Account, inv.Balance, nil
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

// ---- the Shop: packs on sale for points, bought through Rewards ----

// ShopItem is a Shop pack priced in points.
type ShopItem struct {
	GameRef string `json:"game_ref"`
	Price   int    `json:"price"`
}

// shop lists what Rewards sells for this game, cached five minutes (a price change reaches the Shop screen within that).
func (r *rewardsInventory) shop(ctx context.Context) []ShopItem {
	r.mu.Lock()
	if r.now().Before(r.shopUntil) {
		items := r.shopItems
		r.mu.Unlock()
		return items
	}
	r.mu.Unlock()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, r.base+"/internal/v1/adapter/shop?game="+rewardsGame, nil)
	req.Header.Set("X-Internal-Key", r.key)
	// Without an identity Rewards cannot resolve a player: ask with a namespace/subject that never links; the list is the same for everyone.
	q := req.URL.Query()
	q.Set("namespace", "switch")
	q.Set("subject", "shop")
	req.URL.RawQuery = q.Encode()
	resp, err := r.http.Do(req)
	if err != nil || resp.StatusCode != 200 {
		if err == nil {
			resp.Body.Close()
		}
		log.Printf("[klei] rewards shop list failed: %v", err)
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.shopItems // the last good list, or none
	}
	defer resp.Body.Close()
	var out struct {
		Items []struct {
			GameRef string `json:"game_ref"`
			Slot    string `json:"slot"`
			Price   int    `json:"price"`
		} `json:"items"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out) != nil {
		return nil
	}
	var items []ShopItem
	for _, i := range out.Items {
		if i.Slot == "pack" && i.Price > 0 {
			items = append(items, ShopItem{GameRef: i.GameRef, Price: i.Price})
		}
	}
	r.mu.Lock()
	r.shopItems, r.shopUntil = items, r.now().Add(5*time.Minute)
	r.mu.Unlock()
	return items
}

// purchase buys a pack for points. Errors are the game's codes: E_BALANCE_MISMATCH (not enough points), E_PRICE_MISMATCH (the price
// changed, the item is not for sale or is already owned). balance is the player's points after a purchase.
func (r *rewardsInventory) purchase(ctx context.Context, ku, baas, ref, transID string, expected *int) (balance int64, code string) {
	body := map[string]any{"namespace": "switch", "subject": baas, "game": rewardsGame, "game_ref": ref, "trans_id": transID}
	if expected != nil {
		body["expected_price"] = *expected
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, r.base+"/internal/v1/adapter/redeem", bytes.NewReader(b))
	req.Header.Set("X-Internal-Key", r.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		log.Printf("[klei] rewards purchase: %v", err)
		return 0, "E_UNAVAILABLE"
	}
	defer resp.Body.Close()
	var out struct {
		Balance int64  `json:"balance"`
		Code    string `json:"code"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out)
	r.mu.Lock()
	if e, ok := r.by[ku]; ok {
		e.fresh = time.Time{} // the purchase changed what they own and their points
		r.by[ku] = e
	}
	r.mu.Unlock()
	switch {
	case resp.StatusCode == 200:
		return out.Balance, ""
	case resp.StatusCode == 402:
		return out.Balance, "E_BALANCE_MISMATCH"
	default:
		log.Printf("[klei] rewards purchase refused: %d %s", resp.StatusCode, out.Code)
		return 0, "E_PRICE_MISMATCH"
	}
}
