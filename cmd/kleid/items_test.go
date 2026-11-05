package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestItemsProbeIsOffByDefault(t *testing.T) {
	t.Setenv("KLEI_ITEMS", "")
	s := &server{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "http://items.kleientertainment.com/clientitems/DST/GetAllItems", strings.NewReader(`{"ClientToken":"secret"}`))
	s.route(w, r)
	if w.Code != 404 {
		t.Fatalf("default: %d", w.Code)
	}
}

func TestItemsProbeAnswersTheTwoMainMenuCalls(t *testing.T) {
	t.Setenv("KLEI_ITEMS", "probe")
	s := &server{}
	for path, want := range map[string]string{"/iap/DST/GetPricingInfo": `"IAPDefs":[]`, "/clientitems/DST/GetAllItems": `"InventoryStateToken":"openpak-0"`} {
		w := httptest.NewRecorder()
		s.route(w, httptest.NewRequest(http.MethodPost, "http://items.kleientertainment.com"+path, strings.NewReader(`{}`)))
		if w.Code != 200 || !strings.Contains(w.Body.String(), want) || !strings.Contains(w.Body.String(), `"Unchanged":false`) {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	s.route(w, httptest.NewRequest(http.MethodPost, "http://items.kleientertainment.com/clientitems/DST/Swap", strings.NewReader(`{}`)))
	if w.Code != 404 {
		t.Fatalf("other path: %d", w.Code)
	}
}
