package main

import (
	"encoding/base64"
	"testing"
)

func nsaToken(av string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc([]byte(`{"nintendo":{"ai":"010090100e334000","av":"`+av+`"}}`)) + ".sig"
}

// A browser sees only its own version's worlds, and is not cleared to join another's; an
// unknown version still sees and joins everything.
func TestVersionsNeverShareAWorld(t *testing.T) {
	s := newServer(identityClient{}, "secret")
	s.versions.set("KU_host", appVersionClaim(nsaToken("001f0000")))
	s.versions.set("KU_same", appVersionClaim(nsaToken("001F0000")))
	s.versions.set("KU_old", appVersionClaim(nsaToken("001c0000")))
	s.versions.set("KU_unknown", appVersionClaim(nsaToken("0033")))
	lobbyPost(t, s, "/lobby/write", `{"__rowId":"row1","host":"KU_host","v":740510500,"name":"w"}`)

	read := func(ku string) int {
		return len(rowsOf(t, lobbyPost(t, s, "/lobby/read", `{"__token":"`+s.mintToken(ku)+`","query":{"__rowId":"row1"}}`)))
	}
	if read("KU_same") != 1 || read("KU_old") != 0 || read("KU_unknown") != 1 {
		t.Fatalf("read: same %d, old %d, unknown %d", read("KU_same"), read("KU_old"), read("KU_unknown"))
	}

	join := func(ku string) map[string]any {
		return post(t, s, "/login/GameSessionToken", `{"Token":"`+s.mintToken(ku)+`","Server":"KU_host"}`)
	}
	if out := join("KU_old"); out["Error"] != "true" {
		t.Fatalf("other version cleared to join: %#v", out)
	}
	for _, ku := range []string{"KU_same", "KU_unknown"} {
		if out := join(ku); out["Error"] != false {
			t.Fatalf("%s refused: %#v", ku, out)
		}
	}
}
