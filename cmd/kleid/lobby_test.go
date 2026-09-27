package main

import (
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func lobbyPost(t *testing.T, s *server, path, body string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "https://lobby-v2-"+region+".klei.com"+path, strings.NewReader(body))
	s.route(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s answered unparseable JSON %q: %v", path, rec.Body.String(), err)
	}
	return out
}

func rowsOf(t *testing.T, out map[string]any) []any {
	t.Helper()
	rows, ok := out["GET"].([]any)
	if !ok {
		t.Fatalf("read answered %#v, want a GET array", out)
	}
	return rows
}

// A host broadcasts, everyone reads it back — including the fields this service has never heard
// of, which is most of them.
func TestLobbyWriteThenRead(t *testing.T) {
	s := newServer(identityClient{}, "secret")
	out := lobbyPost(t, s, "/lobby/write", `{"__token":"t","__gameId":"DontStarveTogether","__rowId":"row1",
		"masterversion":"1.29.0","name":"hansolo's world","platform":"Switch","connected":1,"season":"autumn"}`)
	if out["result"] != "OK" {
		t.Fatalf("write answered %#v, want result OK", out)
	}
	if out["region"] != region {
		t.Errorf("write answered region %v, want %s", out["region"], region)
	}

	rows := rowsOf(t, lobbyPost(t, s, "/lobby/read", `{"__gameId":"DontStarveTogether"}`))
	if len(rows) != 1 {
		t.Fatalf("read gave %d rows, want 1", len(rows))
	}
	row := rows[0].(map[string]any)
	if row["name"] != "hansolo's world" || row["season"] != "autumn" {
		t.Errorf("the listing came back changed: %#v", row)
	}
	if row["__rowId"] != "row1" {
		t.Errorf("__rowId = %v, want row1", row["__rowId"])
	}
	// The envelope keys are the host's business, not the listing's.
	if _, leaked := row["__token"]; leaked {
		t.Errorf("the host's token was stored in its public listing: %#v", row)
	}

	// keepAlive carries no listing and must not blank the one already there.
	lobbyPost(t, s, "/lobby/keepAlive", `{"__token":"t","__rowId":"row1"}`)
	rows = rowsOf(t, lobbyPost(t, s, "/lobby/read", `{}`))
	if len(rows) != 1 || rows[0].(map[string]any)["name"] != "hansolo's world" {
		t.Errorf("keepAlive lost the listing: %#v", rows)
	}

	lobbyPost(t, s, "/lobby/delete", `{"__token":"t","__rowId":"row1"}`)
	if rows := rowsOf(t, lobbyPost(t, s, "/lobby/read", `{}`)); len(rows) != 0 {
		t.Errorf("delete left %d rows", len(rows))
	}
}

// A Switch browser must not be offered hosts it cannot reach.
func TestLobbyReadFiltersByPlatform(t *testing.T) {
	s := newServer(identityClient{}, "secret")
	lobbyPost(t, s, "/lobby/write", `{"__rowId":"nx","platform":"Switch","name":"switch host"}`)
	lobbyPost(t, s, "/lobby/write", `{"__rowId":"pc","platform":"Steam","name":"steam host"}`)

	rows := rowsOf(t, lobbyPost(t, s, "/lobby/read", `{"query":{"platform":"Switch"}}`))
	if len(rows) != 1 || rows[0].(map[string]any)["name"] != "switch host" {
		t.Fatalf("platform filter gave %#v", rows)
	}
	rows = rowsOf(t, lobbyPost(t, s, "/lobby/read", `{"query":{"__rowId":["pc"]}}`))
	if len(rows) != 1 || rows[0].(map[string]any)["name"] != "steam host" {
		t.Fatalf("row query gave %#v", rows)
	}
}

func TestCDN(t *testing.T) {
	s := newServer(identityClient{}, "secret")
	lobbyPost(t, s, "/lobby/write", `{"__rowId":"nx","platform":"Switch","name":"switch host"}`)

	rec := httptest.NewRecorder()
	s.route(rec, httptest.NewRequest(http.MethodGet, "https://lobby-v2-cdn.klei.com/regioncapabilities-v2.json", nil))
	var caps struct {
		LobbyRegions []struct{ Region string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &caps); err != nil || len(caps.LobbyRegions) == 0 {
		t.Fatalf("region capabilities = %q (%v)", rec.Body.String(), err)
	}
	if caps.LobbyRegions[0].Region != region {
		t.Errorf("region = %q, want %q", caps.LobbyRegions[0].Region, region)
	}

	rec = httptest.NewRecorder()
	s.route(rec, httptest.NewRequest(http.MethodGet, "https://lobby-v2-cdn.klei.com/"+region+"-Switch.json.gz", nil))
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("listings Content-Encoding = %q, want gzip", got)
	}
	gz, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("listings are not gzip: %v", err)
	}
	var out map[string]any
	if err := json.NewDecoder(gz).Decode(&out); err != nil {
		t.Fatalf("listings do not decode: %v", err)
	}
	if rows := rowsOf(t, out); len(rows) != 1 {
		t.Errorf("listings gave %d rows, want the one Switch host", len(rows))
	}
}
