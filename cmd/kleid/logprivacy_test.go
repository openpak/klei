package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every handler, driven end to end with sentinel identities, must leave no account id, Klei id,
// nickname, token, address, world name or free text in the log.
func TestLogsCarryNoPersonalData(t *testing.T) {
	const (
		baasID   = "f00dfacecafe0123"
		nickname = "Zorblaxa"
		pid      = "918273645"
		worldNm  = "SentinelWorldName"
		freeText = "my-private-chat-line"
		clientIP = "203.0.113.77"
		clientPt = "44312"
		auth     = "s3cr3t-bearer"
		cookie   = "cookie-secret"
		query    = "q-token-9f8e7d"
		rowID    = "13285033608306768903"
		guidVal  = "7766554433221100"
		stray    = "unlisted-key-value-xyz"
	)
	s := newServer(stubIdentity(t, 200, `{"pid":`+pid+`,"baas_user_id":"`+baasID+`","nickname":"`+nickname+`"}`), "secret")
	host := kuid(baasID)
	hostTok := s.mintToken(host)
	other := kuid("0123456789abcdef")
	otherTok := s.mintToken(other)

	do := func(method, target, remote, body string) {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.RemoteAddr = remote
		req.Header.Set("Authorization", "Bearer "+auth)
		req.Header.Set("Cookie", "sid="+cookie)
		s.route(httptest.NewRecorder(), req)
	}
	remote := clientIP + ":" + clientPt
	lobby := "https://lobby-v2-" + region + ".klei.com"

	logged := captureLog(t, func() {
		do("POST", "https://login.kleientertainment.com/login/LoginViaSwitch", remote,
			`{"NSAToken":"eyJhbGciOi.eyJzdWIi.c2ln","AccountToken":"eyJ.acct.tok","Game":"`+freeText+`"}`)
		do("POST", "https://login.kleientertainment.com/login/TokenPurpose", remote, `{"Token":"`+hostTok+`"}`)
		do("POST", "https://login.kleientertainment.com/login/TokenPurpose", remote, `{"Token":"`+otherTok+`x"}`)
		do("POST", "https://login.kleientertainment.com/login/GameSessionToken", remote,
			`{"Token":"`+otherTok+`","Server":"`+host+`"}`)
		do("POST", "https://login.kleientertainment.com/login/Nope/"+host, remote, `{"Username":"`+nickname+`"}`)

		do("POST", lobby+"/lobby/write", remote, switchListing(rowID, hostTok, host, worldNm))
		do("POST", lobby+"/lobby/write", remote, `{"__token":"`+hostTok+`","__rowId":"`+rowID+`","__addr":"`+clientIP+`","name":"`+worldNm+`"}`)
		do("POST", lobby+"/lobby/update", remote, `{"__token":"`+hostTok+`","__rowId":"`+rowID+`","players":"return { {name=\"`+nickname+`\"} }"}`)
		do("POST", lobby+"/lobby/keepAlive", remote, `{"__token":"`+hostTok+`","__rowId":"`+rowID+`"}`)
		do("POST", lobby+"/lobby/getIP", remote, `{"__token":"`+hostTok+`","__rowId":"`+rowID+`"}`)
		do("POST", lobby+"/lobby/read", remote, `{"__token":"`+otherTok+`","__gameId":"`+freeText+`"}`)
		do("POST", lobby+"/lobby/read", remote, `{"__token":"not-ours-`+stray+`"}`)
		do("POST", lobby+"/lobby/frobnicate/"+host, remote, `{"anything":"`+freeText+`","nickname":"`+nickname+`"}`)
		do("POST", lobby+"/lobby/write", remote, `not json `+freeText)
		do("POST", lobby+"/lobby/delete", remote, `{"__token":"`+hostTok+`","__rowId":"`+rowID+`","__gameId":"`+freeText+`"}`)
		do("GET", "https://lobby-v2-cdn.klei.com/"+region+"-Switch.json.gz", remote, "")
		do("GET", "https://lobby-v2-cdn.klei.com/other/"+host, remote, "")

		// The unhandled tier: a key nobody has met, free text, form bodies, query strings.
		do("POST", "https://items.kleientertainment.com/clientitems/DST/"+host+"/GetAllItems?ticket="+query+"&who="+nickname,
			remote, `{"ClientToken":"`+hostTok+`","Note":"`+freeText+`","Email":"`+nickname+`@example.test","Count":3,"Events":[{"Event":"COOKBOOK","Detail":"`+stray+`"}]}`)
		do("POST", "https://items.kleientertainment.com/x", remote, "user="+nickname+"&token="+query)
		do("POST", "https://items.kleientertainment.com/y", remote, "free text "+freeText)
	})

	for _, line := range strings.Split(logged, "\n") {
		if line != "" && !strings.Contains(line, "[klei]") {
			t.Errorf("unexpected log line %q", line)
		}
	}
	if !strings.Contains(logged, "LoginViaSwitch") || !strings.Contains(logged, "~") || !strings.Contains(logged, "!! unhandled") {
		t.Fatalf("lines are missing or untagged:\n%s", logged)
	}
	for _, secret := range []string{
		baasID, nickname, pid, worldNm, freeText, clientIP, clientPt, auth, cookie, query, rowID, guidVal, stray,
		host, other, hostTok, otherTok, "eyJhbGciOi", "eyJ.acct", "N:C4arryabHW4BAAAAAAAAAAE", "1155BB968E8485EB",
	} {
		if strings.Contains(logged, secret) {
			t.Errorf("log contains %q:\n%s", secret, logged)
		}
	}
}

// A failed call to nx-baas is a url error: it spells out the URL, whose query a log must not carry.
func TestIdentityErrorsLogNoURL(t *testing.T) {
	s := newServer(identityClient{url: "http://127.0.0.1:1/q-secret-path?k=query-secret"}, "secret")
	logged := captureLog(t, func() {
		req := httptest.NewRequest(http.MethodPost, "https://login.kleientertainment.com/login/LoginViaSwitch",
			strings.NewReader(`{"NSAToken":"eyJ.a.b"}`))
		s.route(httptest.NewRecorder(), req)
	})
	if !strings.Contains(logged, "identity lookup failed") {
		t.Fatalf("no failure logged:\n%s", logged)
	}
	assertNoneIn(t, logged, "q-secret-path", "query-secret", "127.0.0.1")
}
