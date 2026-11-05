package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func itemsPost(t *testing.T, s *server, path string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	s.route(w, httptest.NewRequest(http.MethodPost, "http://items.kleientertainment.com"+path, strings.NewReader(`{"ClientToken":"secret"}`)))
	m := map[string]any{}
	json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func TestItemsAreOffByDefault(t *testing.T) {
	t.Setenv("KLEI_ITEMS", "")
	if code, _ := itemsPost(t, &server{}, "/clientitems/DST/GetAllItems"); code != 404 {
		t.Fatalf("default: %d", code)
	}
}

// The client reads "Error" as a boolean that must be present and false (nso 0x188b04, 0x181574): a string or a
// missing key is an error reply. Item entries need a numeric ItemID and a string ItemType.
func TestItemRepliesHaveTheShapeTheClientReads(t *testing.T) {
	t.Setenv("KLEI_ITEMS", "probe")
	t.Setenv("KLEI_ITEMS_TEST", "wilson_formal, walter_detective")
	s := &server{secret: []byte("test-secret")}
	ku := kuid("baas-1")
	w0 := httptest.NewRecorder()
	s.route(w0, httptest.NewRequest(http.MethodPost, "http://items.kleientertainment.com/clientitems/DST/GetAllItems", strings.NewReader(`{"ClientToken":"`+s.mintToken(ku)+`"}`)))
	var own map[string]any
	json.Unmarshal(w0.Body.Bytes(), &own)
	if own["UserID"] != ku {
		t.Fatalf("UserID = %v, want the user the token names (%s)", own["UserID"], ku)
	}
	code, m := itemsPost(t, s, "/clientitems/DST/GetAllItems")
	if code != 200 || m["Error"] != false || m["Unchanged"] != false {
		t.Fatalf("GetAllItems: %d %v", code, m)
	}
	items, _ := m["Items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items: %v", m["Items"])
	}
	first := items[0].(map[string]any)
	if first["ItemType"] != "wilson_formal" {
		t.Fatalf("item: %v", first)
	}
	if _, ok := first["ItemID"].(float64); !ok {
		t.Fatalf("ItemID is not a number: %v", first["ItemID"])
	}
	if first["ItemID"] == items[1].(map[string]any)["ItemID"] {
		t.Fatal("two item types share an ItemID")
	}
	if code, m = itemsPost(t, s, "/clientitems/DST/DailyGift"); code != 200 || m["Error"] != false || m["GiftReceived"] != false {
		t.Fatalf("DailyGift: %d %v", code, m)
	}
	if code, m = itemsPost(t, s, "/gameevents/DontStarveTogether/KeyValuesGetMulti"); code != 200 || m["Events"] == nil {
		t.Fatalf("KeyValuesGetMulti: %d %v", code, m)
	}
	// The Shop's clock check wants the real time (a fixed value would raise the "clock is wrong" popup), on GET.
	w := httptest.NewRecorder()
	s.route(w, httptest.NewRequest(http.MethodGet, "http://items.kleientertainment.com/iap/dst/GetShopEpoch", nil))
	var ep map[string]any
	json.Unmarshal(w.Body.Bytes(), &ep)
	if tm, ok := ep["Time"].(float64); w.Code != 200 || !ok || tm < float64(time.Now().Unix()-5) || tm > float64(time.Now().Unix()+5) {
		t.Fatalf("GetShopEpoch: %d %s", w.Code, w.Body.String())
	}
	// Nothing to settle: a completed purchase with no items, Error false.
	if code, m = itemsPost(t, s, "/iap/DST/PurchaseCreateAndComplete"); code != 200 || m["Error"] != false || m["Message"] != "Purchase Completed" || len(m["Items"].([]any)) != 0 {
		t.Fatalf("PurchaseCreateAndComplete: %d %v", code, m)
	}
	// Pricing stays a 404 unless asked for; asked for, it lists a def per test item and Error is false.
	if code, _ = itemsPost(t, s, "/iap/DST/GetPricingInfo"); code != 404 {
		t.Fatalf("pricing without the switch: %d", code)
	}
	t.Setenv("KLEI_ITEMS_PRICING", "on")
	code, m = itemsPost(t, s, "/iap/DST/GetPricingInfo")
	defs, _ := m["IAPDefs"].([]any)
	if code != 200 || m["Error"] != false || len(defs) != 3 {
		t.Fatalf("pricing: %d %v", code, m)
	}
	// Nothing for the player: the list is empty but the reply still has Error false.
	t.Setenv("KLEI_ITEMS_TEST", "")
	if _, m = itemsPost(t, s, "/clientitems/DST/GetAllItems"); m["Error"] != false {
		t.Fatalf("empty inventory: %v", m)
	}
	// With no test item the pricing reply still has one def (an empty IAPDefs is an error to the client).
	code, m = itemsPost(t, s, "/iap/DST/GetPricingInfo")
	if defs, _ := m["IAPDefs"].([]any); code != 200 || len(defs) != 1 {
		t.Fatalf("pricing without test items: %d %v", code, m)
	}
}

// KLEI_ITEMS=rewards: GetAllItems lists what Rewards says the player owns (asked by network identity), cached for a minute and
// dropped when the account's entitlement changes; a player Rewards does not know owns nothing.
func TestInventoryFromRewards(t *testing.T) {
	calls := 0
	owned := []string{"wilson_formal"}
	rw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-Internal-Key") != "k" || r.URL.Query().Get("game") != "dst" || r.URL.Query().Get("namespace") != "switch" || r.URL.Query().Get("subject") != "baas-1" {
			http.Error(w, "no", 401)
			return
		}
		o := []map[string]any{}
		for _, t := range owned {
			o = append(o, map[string]any{"game_ref": t})
		}
		json.NewEncoder(w).Encode(map[string]any{"account": "acc-1", "owned": o, "base": []any{}})
	}))
	defer rw.Close()
	t.Setenv("KLEI_ITEMS", "rewards")
	now := time.Now()
	s := &server{secret: []byte("s"), inv: &rewardsInventory{base: rw.URL, key: "k", http: rw.Client(), now: func() time.Time { return now }, by: map[string]invEntry{}}}
	ku := kuid("baas-1")
	s.baas.set(ku, "baas-1")
	list := func() []any {
		w := httptest.NewRecorder()
		s.route(w, httptest.NewRequest(http.MethodPost, "http://items.kleientertainment.com/clientitems/DST/GetAllItems", strings.NewReader(`{"ClientToken":"`+s.mintToken(ku)+`"}`)))
		var m map[string]any
		json.Unmarshal(w.Body.Bytes(), &m)
		if m["Error"] != false {
			t.Fatalf("reply: %v", m)
		}
		return m["Items"].([]any)
	}
	if it := list(); len(it) != 1 || it[0].(map[string]any)["ItemType"] != "wilson_formal" {
		t.Fatalf("owned: %v", it)
	}
	list()
	if calls != 1 {
		t.Fatalf("rewards asked %d times within a minute, want 1", calls)
	}
	owned = []string{"wilson_formal", "walter_formal"}
	s.inv.drop("acc-1") // entitlement_changed
	if it := list(); len(it) != 2 || calls != 2 {
		t.Fatalf("after the event: %v (calls %d)", it, calls)
	}
	// Rewards down: the last answer is served; a player never seen gets nothing.
	rw.Close()
	now = now.Add(2 * time.Minute)
	if it := list(); len(it) != 2 {
		t.Fatalf("outage: %v", it)
	}
	s.baas.set(kuid("baas-2"), "baas-2")
	if got := s.inv.owned(context.Background(), kuid("baas-2"), "baas-2"); len(got) != 0 {
		t.Fatalf("unknown player: %v", got)
	}
}
