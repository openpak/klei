package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubIdentity stands in for nx-baas: it answers /internal/switch/identity with `reply` under
// `status`.
func stubIdentity(t *testing.T, status int, reply string) identityClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/switch/identity" {
			t.Errorf("identity asked for %s", r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return identityClient{url: srv.URL}
}

func post(t *testing.T, s *server, path, body string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "https://login.kleientertainment.com"+path, strings.NewReader(body))
	s.route(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s answered unparseable JSON %q: %v", path, rec.Body.String(), err)
	}
	return out
}

// The client finds the "Error" member and follows the success path only when its value is a JSON
// boolean; a string sends it to the ErrorCode branch. Getting that type wrong is the difference
// between reaching the main menu and "connection to Klei failed", so it is asserted as a type.
func TestLoginViaSwitchSuccess(t *testing.T) {
	s := newServer(stubIdentity(t, 200, `{"pid":1900000001,"baas_user_id":"aaaaaaaaaaaa0001","nickname":"hansolo"}`), "secret")
	out := post(t, s, "/login/LoginViaSwitch", `{"NSAToken":"eyJ.a.b","AccountToken":"eyJ.c.d","Game":"DontStarveTogether"}`)

	if err, ok := out["Error"].(bool); !ok || err {
		t.Fatalf("Error must be the JSON boolean false, got %#v", out["Error"])
	}
	id, _ := out["UserID"].(string)
	if !strings.HasPrefix(id, "KU_") || len(id) != len("KU_")+8 {
		t.Errorf("UserID %q is not a KU_ + 8 character id", id)
	}
	if out["Username"] != "hansolo" {
		t.Errorf("Username = %#v, want the account nickname", out["Username"])
	}
	if tok, _ := out["Token"].(string); s.userForToken(tok) != id {
		t.Errorf("Token %q does not verify back to %q", out["Token"], id)
	}
	// The same console must get the same Klei user on every login: nothing is stored.
	again := post(t, s, "/login/LoginViaSwitch", `{"NSAToken":"eyJ.a.b"}`)
	if again["UserID"] != out["UserID"] {
		t.Errorf("second login gave %v, want the same id %v", again["UserID"], out["UserID"])
	}
}

func TestLoginViaSwitchFailures(t *testing.T) {
	for _, tc := range []struct {
		name, reply, body, code string
		status                  int
	}{
		{"rejected token", `{"error":"id_token not proven"}`, `{"NSAToken":"nope"}`, "E_INVALID_CLIENT_TOKEN", 401},
		{"banned", `{"error":"account_banned"}`, `{"NSAToken":"eyJ.a.b"}`, "E_BANNED_GAME", 403},
		{"no token", `{}`, `{"AccountToken":"eyJ.c.d"}`, "E_MISSING_FIELD", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(stubIdentity(t, tc.status, tc.reply), "secret")
			out := post(t, s, "/login/LoginViaSwitch", tc.body)
			// A failure's "Error" is deliberately a string: that is what makes it not a boolean,
			// which is how the client tells the two apart.
			if _, isString := out["Error"].(string); !isString {
				t.Fatalf("Error must be a string on failure, got %#v", out["Error"])
			}
			if out["ErrorCode"] != tc.code {
				t.Errorf("ErrorCode = %v, want %s", out["ErrorCode"], tc.code)
			}
		})
	}
}

// A KU id is base64url, so it can contain '-' and '_'; only '.' is safe as the token separator.
func TestTokenWithDashInID(t *testing.T) {
	s := newServer(identityClient{}, "secret")
	ku := "KU_-590hZEj"
	if got := s.userForToken(s.mintToken(ku)); got != ku {
		t.Errorf("userForToken round trip gave %q, want %q", got, ku)
	}
	if got := s.userForToken(ku + ".forged"); got != "" {
		t.Errorf("a forged token verified as %q", got)
	}
}

// A host calls TokenPurpose while starting its server and will not start without an answer. The
// game name is checked by the client, and "Error" is the same boolean switch as the login.
func TestTokenPurpose(t *testing.T) {
	s := newServer(stubIdentity(t, 200, `{"pid":1,"baas_user_id":"aaaaaaaaaaaa0001","nickname":"hansolo"}`), "secret")
	login := post(t, s, "/login/LoginViaSwitch", `{"NSAToken":"eyJ.a.b"}`)
	token, _ := login["Token"].(string)

	out := post(t, s, "/login/TokenPurpose", `{"Token":"`+token+`"}`)
	if err, ok := out["Error"].(bool); !ok || err {
		t.Fatalf("Error must be the JSON boolean false, got %#v", out["Error"])
	}
	if out["Game"] != "DontStarveTogether" {
		t.Errorf("Game = %#v; the client compares it by name and refuses a mismatch", out["Game"])
	}
	if out["UserID"] != login["UserID"] {
		t.Errorf("UserID = %v, want the id the login minted (%v)", out["UserID"], login["UserID"])
	}
	// The name comes back from the login, not from a second identity lookup.
	if out["Username"] != "hansolo" {
		t.Errorf("Username = %#v, want the name the login gave", out["Username"])
	}

	// The other action on this path, GenerateClusterToken, reads Token and says
	// "[Error] No cluster_token received" without it — which stopped the server starting even
	// after TokenPurpose itself was answered.
	if tok, _ := out["Token"].(string); s.userForToken(tok) != login["UserID"] {
		t.Errorf("Token = %#v, want a cluster token for this player", out["Token"])
	}

	// A token this service did not mint is not an identity.
	bad := post(t, s, "/login/TokenPurpose", `{"Token":"KU_someoneelse.forged"}`)
	if bad["ErrorCode"] != "E_WRONG_TOKEN_PURPOSE" {
		t.Errorf("a forged token answered %#v", bad)
	}
}

// A game session token must name the host it was minted for, and must not validate for any
// other one — the client refuses a mismatch with "session host mismatch" and the join dies.
func TestGameSessionTokenBindsToItsHost(t *testing.T) {
	s := newServer(identityClient{}, "secret")
	sess := s.mintToken("KU_player00")

	out := post(t, s, "/login/GameSessionToken",
		`{"Token":"`+sess+`","Server":"KU_hostaaaa"}`)
	if out["Error"] != false {
		t.Fatalf("GameSessionToken answered %#v, want Error false", out)
	}
	tok, _ := out["Token"].(string)
	if tok == "" {
		t.Fatal("no Token in reply; the client refuses an empty GameSessionToken")
	}
	if out["GameSessionToken"] != out["Token"] {
		t.Errorf("GameSessionToken %v != Token %v", out["GameSessionToken"], out["Token"])
	}

	got := post(t, s, "/login/ValidateToken", `{"Token":"`+tok+`"}`)
	if got["Error"] != false || got["Server"] != "KU_hostaaaa" {
		t.Fatalf("ValidateToken answered %#v, want Error false and Server KU_hostaaaa", got)
	}

	// Same player, different host: the HMAC covers the pair, so this one is not ours.
	other := s.mintGameSession("KU_player00", "KU_hostbbbb")
	if _, host := s.gameSessionParts(other[:len(other)-4] + "AAAA"); host != "" {
		t.Error("a tampered game session token validated")
	}
	if _, host := s.gameSessionParts(other); host != "KU_hostbbbb" {
		t.Errorf("host = %q, want KU_hostbbbb", host)
	}
}
