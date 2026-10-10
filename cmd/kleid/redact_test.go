package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureLog collects what the handlers log while f runs.
func captureLog(t *testing.T, f func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	f()
	return buf.String()
}

func assertNoneIn(t *testing.T, logged string, secrets ...string) {
	t.Helper()
	for _, sec := range secrets {
		if strings.Contains(logged, sec) {
			t.Errorf("the log carries %q:\n%s", sec, logged)
		}
	}
}

// The item server's requests reach the unhandled path with the player's session token in them.
func TestUnhandledPostLogsNoToken(t *testing.T) {
	s := newServer(identityClient{}, "secret")
	tok := s.mintToken("KU_abcdEFGH")
	logged := captureLog(t, func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost,
			"https://items.kleientertainment.com/clientitems/DST/GetAllItems?ticket=qwertyuiop123&Game=DST",
			strings.NewReader(`{"CheckTwitchEntitlements":true,"ClientToken":"`+tok+`","InventoryStateToken":"",
				"Events":[{"Token":"nested-secret-value","Event":"COOKBOOK"}],"UserID":"KU_abcdEFGH"}`))
		s.route(rec, req)
	})
	if !strings.Contains(logged, "!! unhandled") || !strings.Contains(logged, "ClientToken") {
		t.Fatalf("the unhandled request was not logged with its keys:\n%s", logged)
	}
	if !strings.Contains(logged, "Game=DST") || !strings.Contains(logged, "COOKBOOK") {
		t.Errorf("redaction took the harmless fields too:\n%s", logged)
	}
	assertNoneIn(t, logged, tok, "KU_abcdEFGH", "nested-secret-value", "qwertyuiop123")
}

func TestUnimplementedLoginLogsNoPassword(t *testing.T) {
	s := newServer(identityClient{}, "secret")
	logged := captureLog(t, func() {
		post(t, s, "/login/Login", `{"Username":"someone","Password":"hunter2hunter2","Token":"eyJhbGciOi.eyJzdWIi.c2ln"}`)
	})
	assertNoneIn(t, logged, "someone", "hunter2hunter2", "eyJhbGciOi.eyJzdWIi.c2ln")
}

// A host's write carries its session token in the envelope and its name, netid and Klei id in
// the listing. The listing is still logged -- redacted, keys kept.
func TestLobbyLogsNoTokenOrIdentity(t *testing.T) {
	s := newServer(identityClient{}, "secret")
	tok := s.mintToken("KU_hostHOST")
	reader := s.mintToken("KU_readREAD")
	logged := captureLog(t, func() {
		lobbyPost(t, s, "/lobby/write", switchListing("13285033608306768903", tok, "KU_hostHOST", "Hostname's World"))
		lobbyPost(t, s, "/lobby/write", `{"__token":"not-ours-but-still-secret","__rowId":"x","name":"y"}`)
		lobbyPost(t, s, "/lobby/update", `{"__token":"`+tok+`","__rowId":"13285033608306768903","players":"return { {name=\"Hostname\"} }"}`)
		lobbyPost(t, s, "/lobby/read", `{"__token":"`+reader+`","__gameId":"DontStarveTogether"}`)
		cdnListing(t, s)
	})
	if !strings.Contains(logged, "lobby write listing") || !strings.Contains(logged, `"__token":"<redacted `) {
		t.Fatalf("the write listing was not logged with its keys:\n%s", logged)
	}
	assertNoneIn(t, logged, tok, reader, "not-ours-but-still-secret", "KU_hostHOST", "KU_readREAD",
		"Hostname", "N:C4arryabHW4BAAAAAAAAAAE", "1155BB968E8485EB")
}

func TestRedactBodyKeepsShape(t *testing.T) {
	got := redactBody([]byte(`{"__token":"abcdef","password":true,"port":10999,"intent":"survival"}`))
	for _, want := range []string{`"__token":"<redacted 6>"`, `"password":true`, `"port":10999`, `"intent":"survival"`} {
		if !strings.Contains(got, want) {
			t.Errorf("redactBody = %s, want it to contain %s", got, want)
		}
	}
	if got := redactBody([]byte("token=abc123&x=1")); got != "token=<redacted 6>&x=1" {
		t.Errorf("form body = %q", got)
	}
}
