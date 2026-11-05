package main

// items.kleientertainment.com -- skins and inventory. Card openpak/prds#141 (Rewards unlocks game cosmetics).
//
// Off by default: the client carries on after a 404, and a wrong reply is worse than none. With KLEI_ITEMS=probe the
// two calls the client makes at the main menu are answered with an empty, well-formed reply, so the next Harbor run
// shows how the client reacts (client_log.txt) before any item is served. The reply keys come from the Switch build
// (nso 0x17f9d4 ProcessPricingData, 0x1882cc GetAllItems_Complete); see docs/protocol.md section 4a. The item list
// itself is NOT answered yet: its key is not recovered.

import (
	"net/http"
	"os"
	"strings"
)

func itemsMode() string { return strings.ToLower(os.Getenv("KLEI_ITEMS")) }

// items answers items.kleientertainment.com. It reports false when the request is not one it handles, so the caller
// falls through to the logged 404.
func (s *server) items(w http.ResponseWriter, r *http.Request) bool {
	if itemsMode() != "probe" || r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/iap/DST/GetPricingInfo":
		// IAPDefs and BarterDefs empty: nothing is for sale here (points are the only currency, and they live in Rewards).
		writeJSON(w, 200, map[string]any{"Error": "", "Unchanged": false, "PricingHash": 1, "IAPDefs": []any{}, "BarterDefs": []any{}})
		return true
	case "/clientitems/DST/GetAllItems":
		writeJSON(w, 200, map[string]any{"Error": "", "Unchanged": false, "InventoryStateToken": "openpak-0", "EventHash": ""})
		return true
	}
	return false
}
