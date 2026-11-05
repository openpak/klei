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
	"hash/fnv"
	"net/http"
	"os"
	"strings"
)

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
func inventoryReply(types []string) map[string]any {
	items := make([]map[string]any, 0, len(types))
	for _, t := range types {
		items = append(items, map[string]any{"ItemID": itemID(t), "ItemType": t})
	}
	return map[string]any{"Error": false, "Unchanged": false, "InventoryStateToken": "openpak-1", "Items": items}
}

// items answers items.kleientertainment.com. It reports false when the request is not one it handles, so the caller
// falls through to the logged 404.
func (s *server) items(w http.ResponseWriter, r *http.Request) bool {
	if itemsMode() != "probe" || r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/clientitems/DST/GetAllItems":
		writeJSON(w, 200, inventoryReply(testItems()))
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
		for _, t := range testItems() {
			defs = append(defs, map[string]any{"ItemType": t, "KleiCurrency": "KLEI_GEMS", "KleiCurrencyCost": 0})
		}
		writeJSON(w, 200, map[string]any{"Error": false, "Unchanged": false, "PricingHash": 1, "IAPDefs": defs})
		return true
	}
	return false
}
