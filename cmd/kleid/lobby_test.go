package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
		"__addr":"1.2.3.4","masterversion":"1.29.0","name":"hansolo's world","platform":"Switch","connected":1,"season":"autumn"}`)
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
	if row["__addr"] != "1.2.3.4" {
		t.Errorf("__addr = %v, want 1.2.3.4 — the join address must survive the write", row["__addr"])
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

// A master shard takes its row id from POST.__rowId and will not read its shard link without
// one; a secondary then asks getIP for a bare __addr. Without both, a world with caves hangs
// at launch.
func TestLobbyGivesShardsTheirRowAndAddress(t *testing.T) {
	s := newServer(identityClient{}, "secret")
	out := lobbyPost(t, s, "/lobby/write", `{"__rowId":"row1","platform":"Switch"}`)
	post, _ := out["POST"].(map[string]any)
	if post["__rowId"] != "row1" {
		t.Fatalf("write answered %#v, want POST.__rowId row1", out)
	}
	ip := lobbyPost(t, s, "/lobby/getIP", `{"__rowId":"row1","region":"us-east-1"}`)
	if a, _ := ip["__addr"].(string); a == "" {
		t.Fatalf("getIP answered %#v, want a top-level __addr string", ip)
	}
}

// switchListing is the shape a Switch host writes (docs/protocol.md §3, captured 2026-09-29),
// with stand-in values.
func switchListing(guid, token, host, name string) string {
	return `{"__token":"` + token + `","__gameId":"DontStarveTogether","masterversion":1,
		"allownewplayers":true,"clanonly":false,"clienthosted":true,"clientmodsoff":false,"connected":0,
		"data":"return { day=43 }","dedicated":true,"desc":"","fo":false,"guid":"` + guid + `",
		"host":"` + host + `","intent":"survival","lanonly":false,"maxconnections":4,"mode":"survival",
		"mods":false,"mods_info":[],"name":"` + name + `","nat":5,"password":false,"platform":32,
		"players":"return {  }","port":10999,"pvp":false,"season":"spring","secondaries":{},
		"serverpaused":false,"session":"1155BB968E8485EB","steamid":"N:C4arryabHW4BAAAAAAAAAAE",
		"steamroom":"680893","tags":"WS:AA,english,survival","tick":15,"v":740510500,
		"worldgen":"return {str=\"eJw=\"}"}`
}

// cdnListing fetches the pre-baked Switch listing file the browser shows, raw and decoded.
func cdnListing(t *testing.T, s *server) (string, map[string]map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.route(rec, httptest.NewRequest(http.MethodGet, "https://lobby-v2-cdn.klei.com/"+region+"-Switch.json.gz", nil))
	gz, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("listings are not gzip: %v", err)
	}
	raw, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("listings do not gunzip: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("listings do not decode: %v", err)
	}
	byRow := map[string]map[string]any{}
	for _, r := range rowsOf(t, out) {
		row := r.(map[string]any)
		byRow[row["__rowId"].(string)] = row
	}
	return string(raw), byRow
}

// The sequence a Switch host sends (2026-09-30 and 2026-10-10): one write with the whole
// listing, then updates carrying two to five fields and keepAlives carrying none. The browser
// needs the whole listing every time it looks; a row of data/players/secondaries alone is
// dropped, and the joiner sees "0 of 0 shown".
func TestLobbyUpdatesMergeIntoTheWrite(t *testing.T) {
	s := newServer(identityClient{}, "secret")
	tok := s.mintToken("KU_hostHOST")
	const a, b = "13285033608306768903", "99999999999999999999"
	out := lobbyPost(t, s, "/lobby/write", switchListing(a, tok, "KU_hostHOST", "Host's World"))
	if post, _ := out["POST"].(map[string]any); post["__rowId"] != a {
		t.Fatalf("write answered %#v, want POST.__rowId %s", out, a)
	}
	lobbyPost(t, s, "/lobby/write", switchListing(b, tok, "KU_otherOTH", "Other World"))
	lobbyPost(t, s, "/lobby/update", `{"__token":"`+tok+`","__rowId":"`+a+`"}`)
	lobbyPost(t, s, "/lobby/update", `{"__token":"`+tok+`","__rowId":"`+a+`","connected":1,
		"data":"return { day=44 }","players":"return { {name=\"p\"} }","secondaries":{"Caves":{}},"serverpaused":false}`)
	lobbyPost(t, s, "/lobby/keepAlive", `{"__token":"`+tok+`","__rowId":"`+a+`"}`)
	lobbyPost(t, s, "/lobby/update", `{"__token":"`+tok+`","__rowId":"`+a+`","data":"return { day=45 }","players":"return {  }"}`)
	lobbyPost(t, s, "/lobby/keepAlive", `{"__token":"`+tok+`","__rowId":"`+a+`"}`)

	raw, rows := cdnListing(t, s)
	if len(rows) != 2 {
		t.Fatalf("listing has %d rows, want both worlds: %s", len(rows), raw)
	}
	row := rows[a]
	want := map[string]any{
		"name": "Host's World", "intent": "survival", "mode": "survival", "port": float64(10999),
		"v": float64(740510500), "steamid": "N:C4arryabHW4BAAAAAAAAAAE", "host": "KU_hostHOST",
		"guid": a, "session": "1155BB968E8485EB", "platform": float64(32), "nat": float64(5),
		"maxconnections": float64(4), "password": false, "allownewplayers": true,
		"data": "return { day=45 }", "connected": float64(1), "players": "return {  }",
	}
	for k, v := range want {
		if row[k] != v {
			t.Errorf("row %s: %s = %#v, want %#v", a, k, row[k], v)
		}
	}
	if len(row) != 35 { // 34 listing fields the host wrote (envelope dropped) + __rowId
		t.Errorf("row %s has %d keys, want 35: %#v", a, len(row), row)
	}
	if rows[b]["name"] != "Other World" || rows[b]["data"] != "return { day=43 }" || rows[b]["connected"] != float64(0) {
		t.Errorf("the other world was touched by this one's updates: %#v", rows[b])
	}
	// The listing file is public: the host's token has no business in it.
	for _, leak := range []string{tok, "__token", "__gameId", "masterversion"} {
		if strings.Contains(raw, leak) {
			t.Errorf("the public listing carries %q: %s", leak, raw)
		}
	}
}

// A host is silent for up to five minutes between its updates and keepAlives, and longer once
// its world is paused. It must stay listed through that, and a host that does fall out of the
// browser comes back whole with its next keepAlive.
func TestLobbyListingSurvivesTheHostsGaps(t *testing.T) {
	s := newServer(identityClient{}, "secret")
	const a = "13285033608306768903"
	lobbyPost(t, s, "/lobby/write", switchListing(a, "t", "KU_hostHOST", "Host's World"))
	age := func(d time.Duration) {
		s.lobbies.mu.Lock()
		l := s.lobbies.rows[a]
		l.seen = time.Now().Add(-d)
		s.lobbies.rows[a] = l
		s.lobbies.mu.Unlock()
	}

	age(9 * time.Minute) // a keepAlive period with no update in it
	if _, rows := cdnListing(t, s); rows[a]["name"] != "Host's World" {
		t.Fatalf("a host nine minutes into its keepAlive period is gone: %#v", rows)
	}

	age(listingTTL + time.Minute)
	if _, rows := cdnListing(t, s); len(rows) != 0 {
		t.Fatalf("an expired host is still listed: %#v", rows)
	}
	lobbyPost(t, s, "/lobby/keepAlive", `{"__rowId":"`+a+`"}`)
	if _, rows := cdnListing(t, s); rows[a]["name"] != "Host's World" || rows[a]["port"] != float64(10999) {
		t.Fatalf("a keepAlive after expiry did not bring the whole listing back: %#v", rows)
	}
}

// A row nobody wrote (the host wrote before this process started) has nothing the browser can
// show, so it is not served -- and it does not stop the host's next write from listing it.
func TestLobbyUnwrittenRowIsNotServed(t *testing.T) {
	s := newServer(identityClient{}, "secret")
	const a = "13285033608306768903"
	lobbyPost(t, s, "/lobby/update", `{"__rowId":"`+a+`","data":"return { day=2 }","players":"return {  }","secondaries":{}}`)
	lobbyPost(t, s, "/lobby/keepAlive", `{"__rowId":"`+a+`"}`)
	if _, rows := cdnListing(t, s); len(rows) != 0 {
		t.Fatalf("a row with no write was served: %#v", rows)
	}
	lobbyPost(t, s, "/lobby/write", switchListing(a, "t", "KU_hostHOST", "Host's World"))
	if _, rows := cdnListing(t, s); rows[a]["name"] != "Host's World" || rows[a]["data"] != "return { day=43 }" {
		t.Fatalf("the write did not list the row whole: %#v", rows)
	}
}

// A second write is a new world start with a whole listing, so it replaces the row: a field the
// old world had and the new one lacks must not linger.
func TestLobbyWriteReplaces(t *testing.T) {
	s := newServer(identityClient{}, "secret")
	lobbyPost(t, s, "/lobby/write", `{"__rowId":"r","name":"one","stale":"x"}`)
	lobbyPost(t, s, "/lobby/write", `{"__rowId":"r","name":"two"}`)
	rows := rowsOf(t, lobbyPost(t, s, "/lobby/read", `{}`))
	if len(rows) != 1 {
		t.Fatalf("rows = %#v", rows)
	}
	if row := rows[0].(map[string]any); row["name"] != "two" || row["stale"] != nil {
		t.Errorf("second write gave %#v, want it to replace the first", row)
	}
}
