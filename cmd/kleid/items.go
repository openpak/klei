package main

// items.kleientertainment.com -- skins and inventory. Card openpak/prds#141 (Rewards unlocks game cosmetics).
//
// Off by default: the client carries on after a 404. KLEI_ITEMS=probe answers the calls the client makes at the main
// menu with the shapes recovered from the Switch build (docs/protocol.md section 4a). The first probe (v0.6.0) sent
// "Error": "" and an empty IAPDefs; on a real console the skins screen then said "failed to download your item
// collection". The client reads Error as a boolean that must be present and false, so that reply was an error reply.
//
// KLEI_ITEMS_TEST is a comma-separated list of DST item types (skin names such as "wilson_formal") every account is
// shown as owning, for the test run on Harbor and the console. The per-account list from Rewards replaces it.

import (
	"encoding/json"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// itemsMode is "" (off: the client's 404s), "probe" (the test list in KLEI_ITEMS_TEST) or "rewards" (each player's cosmetics
// from Rewards). Both non-empty modes answer the same startup calls.
func itemsMode() string { return strings.ToLower(os.Getenv("KLEI_ITEMS")) }

// testItems are the item types the probe grants to everyone.
func testItems() []string {
	var out []string
	for _, t := range strings.Split(os.Getenv("KLEI_ITEMS_TEST"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// itemID is a stable number for an item type: the client wants a number per owned copy.
func itemID(itemType string) int64 {
	h := fnv.New32a()
	h.Write([]byte(itemType))
	return 1_000_000 + int64(h.Sum32()%1_000_000_000)
}

// inventoryReply is GetAllItems' answer: Error false and the full list (Unchanged false, so the client always
// reads the list). Item entries carry ItemID (number) and ItemType (string); both are required by the client.
func inventoryReply(user string, types []string, points *int64) map[string]any {
	items := make([]map[string]any, 0, len(types))
	for _, t := range types {
		items = append(items, map[string]any{"ItemID": itemID(t), "ItemType": t})
	}
	// UserID is the Klei user id the login returned. The inventory processor (nso 0x188b04, the branch at 0x188d50)
	// compares it with the signed-in user's id and rejects the whole list on a mismatch or a missing id; that is
	// what made the skins screen say "failed to download your item collection" with an otherwise valid reply.
	reply := map[string]any{"Error": false, "Unchanged": false, "UserID": user, "InventoryStateToken": "openpak-1", "Items": items}
	if points != nil {
		// The game's Shop currency is "BOLTS" (CurrencyMap, nso 0x18bbcc); here it is the player's Rewards points.
		reply["CurrencyMap"] = map[string]any{"BOLTS": *points}
	}
	return reply
}

// clientUser is the Klei user the request's ClientToken (the session token LoginViaSwitch minted) names, "" if none.
func (s *server) clientUser(r *http.Request) string {
	var in struct{ ClientToken string }
	if json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in) != nil {
		return ""
	}
	return s.userForToken(in.ClientToken)
}

// items answers items.kleientertainment.com. It reports false when the request is not one it handles, so the caller
// falls through to the logged 404.
func (s *server) items(w http.ResponseWriter, r *http.Request) bool {
	if m := itemsMode(); m != "probe" && m != "rewards" {
		return false
	}
	// The Shop screen asks for the server's clock and warns the player when it is more than 60 s from the console's
	// (purchasepackscreen.lua, PurchasePackScreen constructor): the answer is the current time, never a fixed value.
	// The path is lower case "dst" there, and the console sends GET although the script says POST.
	if r.URL.Path == "/iap/dst/GetShopEpoch" {
		writeJSON(w, 200, map[string]any{"Time": time.Now().Unix()})
		return true
	}
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/iap/DST/PurchaseWithCurrency":
		// Buying a pack in the Shop with points. Body: ItemType, TransID, ExpectedPrice, ClientToken (nso 0x266430). Reply: Error false and the
		// new balance, or Error true with the game's code (nso 0x173090). Only in rewards mode; real money never reaches here.
		if itemsMode() != "rewards" || s.inv == nil {
			return false
		}
		var in struct {
			ItemType      string
			TransID       string
			ClientToken   string
			ExpectedPrice *int
		}
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		json.Unmarshal(raw, &in)
		ku := s.userForToken(in.ClientToken)
		baas := s.baas.get(ku)
		if ku == "" || baas == "" || in.ItemType == "" {
			writeJSON(w, 200, map[string]any{"Error": true, "ErrorCode": "E_PRICE_MISMATCH"})
			return true
		}
		bal, code := s.inv.purchase(r.Context(), ku, baas, in.ItemType, in.TransID, in.ExpectedPrice)
		if code != "" {
			writeJSON(w, 200, map[string]any{"Error": true, "ErrorCode": code})
			return true
		}
		writeJSON(w, 200, map[string]any{"Error": false, "NewCurrency": bal})
		return true
	case "/iap/DST/PurchaseCreateAndComplete":
		// Asked at every start ("[NX Store] Checking for unconsumed purchases..."): "settle the eShop purchases I have not
		// consumed yet". OpenPak sells nothing for money, so there is nothing to complete: a finished purchase with no items.
		// The body carries the console's eShop auth token; it is never read or logged (the log shows the redacted body).
		writeJSON(w, 200, map[string]any{"Error": false, "Message": "Purchase Completed", "Items": []any{}})
		return true
	case "/clientitems/DST/GetAllItems":
		ku := s.clientUser(r)
		types := testItems()
		var points *int64
		if itemsMode() == "rewards" {
			types = nil
			if s.inv != nil && ku != "" {
				t, p := s.inv.owned(r.Context(), ku, s.baas.get(ku))
				types, points = t, &p
			}
		}
		writeJSON(w, 200, inventoryReply(ku, types, points))
		return true
	case "/clientitems/DST/DailyGift":
		// GiftReceived true would hand the player an item; there is no daily gift.
		writeJSON(w, 200, map[string]any{"Error": false, "GiftReceived": false})
		return true
	case "/gameevents/DontStarveTogether/KeyValuesGetMulti":
		// Nothing stored: the client reads Events (each with Revision and KeyValues) and starts empty.
		writeJSON(w, 200, map[string]any{"Events": []any{}})
		return true
	case "/iap/DST/GetPricingInfo":
		if os.Getenv("KLEI_ITEMS_PRICING") != "on" {
			return false
		}
		// The client wants at least one IAP def, each with an ItemType (string); KleiCurrency plus its cost make a def.
		defs := []map[string]any{}
		var priced []string
		if itemsMode() == "probe" {
			priced = testItems()
		}
		if itemsMode() == "rewards" && s.inv != nil {
			// The Shop sells what Rewards sells, priced in points (the game's BOLTS). The Shop screen draws each pack from its own
			// strings and art, so only packs the game knows are listed.
			for _, it := range s.inv.shop(r.Context()) {
				defs = append(defs, map[string]any{"ItemType": it.GameRef, "KleiCurrency": "BOLTS", "KleiCurrencyCost": it.Price, "KleiCurrencySaleCost": it.Price})
			}
		}
		for _, t := range priced {
			// Never free: the Shop only lists item types it has strings for (MISC_ITEMS: chests and packs), so a skin's
			// def is not shown; the price is a guard if one ever were, and nothing here can be bought.
			defs = append(defs, map[string]any{"ItemType": t, "KleiCurrency": "KLEI_GEMS", "KleiCurrencyCost": 999999})
		}
		// The client rejects an empty list ("Missing IAPDefs"), and an item type the game has no strings for is skipped by
		// the Shop screen ("Missing def for IAP"): this one keeps the list non-empty and is never shown or sold.
		defs = append(defs, map[string]any{"ItemType": "openpak_none", "KleiCurrency": "KLEI_GEMS", "KleiCurrencyCost": 999999})
		writeJSON(w, 200, map[string]any{"Error": false, "Unchanged": false, "PricingHash": 1, "IAPDefs": defs})
		return true
	}
	return false
}
