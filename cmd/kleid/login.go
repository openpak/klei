package main

// login.kleientertainment.com — the only thing standing between a Switch DST and its main menu.
//
// The game POSTs /login/LoginViaSwitch with the console's own tokens and expects a Klei user
// back. Real Klei answers this with HTTP 500 for an OpenPak console (measured 2026-09-27): the
// NSAToken is genuine but signed by us, so Nintendo cannot vouch for it and Klei will not mint a
// user. That 500 is the "connection to Klei failed" on screen.
//
// The response contract below is not guessed. It was read out of the Switch build's own
// executable (v1.29.0, main.nso) — see docs/protocol.md §2 for the disassembly this comes from.
// Two things in it are load-bearing:
//
//   - "Error" must be present and a JSON **boolean**. The client finds that member, checks its
//     type is boolean, and only then follows the success path; any other type (a string "true",
//     say) sends it to the ErrorCode branch. A response without "Error" at all is a failure too.
//   - The fields it then reads are Token, UserID, Username, AnalyticsJWT and AuthProviderEnv,
//     each as a string. A missing one is empty, not fatal.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
)

func newServer(id identityClient, secret string) *server {
	key := []byte(secret)
	if len(key) == 0 {
		// A boot-local secret is enough: a token outlives nothing but the process, and the game
		// logs in again on its next launch. A shared secret only matters once a second service
		// has to verify these tokens.
		key = make([]byte, 32)
		_, _ = rand.Read(key)
		log.Printf("[klei] no -token-secret: session tokens are valid for this process only")
	}
	return &server{ids: id, secret: key}
}

// login handles every /login/… path. Only LoginViaSwitch is implemented; the rest are logged in
// full, because what an unimplemented one is asked for is how it gets written.
func (s *server) login(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/login/LoginViaSwitch":
		s.loginViaSwitch(w, r)
	case "/login/TokenPurpose":
		s.tokenPurpose(w, r)
	case "/login/GameSessionToken":
		s.gameSessionToken(w, r)
	default:
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		log.Printf("[klei] !! login %s %s not implemented, body=%s", r.Method, r.URL.Path, summarise(body))
		loginError(w, "E_NOT_IMPLEMENTED")
	}
}

func (s *server) loginViaSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		loginError(w, "E_INVALID_INPUT")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var in struct {
		// The NSA id_token: nn::account's NetworkServiceAccount token, which on an OpenPak
		// console is one nx-baas signed.
		NSAToken string `json:"NSAToken"`
		// The Nintendo Account authorization token the game asks for alongside it. Klei uses it
		// to tie the Klei account to a Nintendo Account; OpenPak needs no second proof, and the
		// NSA token already names the player, so this is logged and not required.
		AccountToken string `json:"AccountToken"`
		Game         string `json:"Game"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		log.Printf("[klei] LoginViaSwitch: unreadable body (%d bytes): %v", len(body), err)
		loginError(w, "E_INVALID_INPUT")
		return
	}
	if in.NSAToken == "" {
		log.Printf("[klei] LoginViaSwitch: no NSAToken in body=%s", summarise(body))
		loginError(w, "E_MISSING_FIELD")
		return
	}

	p, err := s.ids.byIDToken(in.NSAToken)
	switch {
	case errors.Is(err, errTokenRejected):
		log.Printf("[klei] LoginViaSwitch: nx-baas rejected the NSAToken: %v", err)
		loginError(w, "E_INVALID_CLIENT_TOKEN")
		return
	case errors.Is(err, errAccountBanned):
		log.Printf("[klei] LoginViaSwitch: account banned: %v", err)
		loginError(w, "E_BANNED_GAME")
		return
	case err != nil:
		log.Printf("[klei] LoginViaSwitch: identity lookup failed: %v", err)
		loginError(w, "E_DB_FAIL")
		return
	}

	ku := kuid(p.BaasUserID)
	name := p.Nickname
	if name == "" {
		name = ku
	}
	s.names.set(ku, name)

	log.Printf("[klei] LoginViaSwitch: game=%q pid=%d %s -> %s (%s), account token %s",
		in.Game, p.PID, p.BaasUserID, ku, name, present(in.AccountToken))

	writeJSON(w, 200, map[string]any{
		"Error":    false, // a JSON boolean, and the whole success/failure switch (§2)
		"Token":    s.mintToken(ku),
		"UserID":   ku,
		"Username": name,
		// Klei's analytics and environment fields. Nothing in the client requires them to carry
		// anything, and OpenPak has no analytics to point at, so they stay empty and "prod".
		"AnalyticsJWT":    "",
		"AuthProviderEnv": "prod",
	})
}

// tokenPurpose answers "whose token is this, and for which game". A DST host calls it while
// starting its server — twice, once per shard — and refuses to start when it does not get an
// answer: "Server failed to start", which is how this gap reached the player.
//
// The reply carries the same identity the login minted, plus the game the token is good for.
// The client compares that against its own "DontStarveTogether" and logs
// "[ACCOUNT_ACTION_TOKEN_PURPOSE] Game mismatch failure, expected %s, received %s" when they
// differ, so the name is not decoration (nso 0x21e2d4–0x21e3dc). It then logs
// "Received (%s) from TokenPurpose" with the UserID and goes on to start the server.
func (s *server) tokenPurpose(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var in struct {
		Token string `json:"Token"`
	}
	_ = json.Unmarshal(body, &in)

	// A THIRD action posts here, and it is the one that finishes a join: the server a player is
	// connecting to sends the player's *game session* token back to be checked. Measured
	// 2026-09-29 -- there is no /login/ValidateToken, it is this path again. The reply is read
	// by ValidateGameSessionTokenResponse, which checks Error is a boolean and then compares
	// "Server" to the server's own id; a mismatch is "Failed to set game session token, session
	// host mismatch" (nso 0x171ae4) and the player sees "Authentication failed".
	if joiner, host := s.gameSessionParts(in.Token); host != "" {
		log.Printf("[klei] TokenPurpose/validate: %s is cleared for server %s", joiner, host)
		writeJSON(w, 200, map[string]any{
			"Error":           false,
			"Server":          host,
			"Game":            dstGame,
			"UserID":          joiner,
			"Username":        s.names.get(joiner),
			"AnalyticsJWT":    "",
			"AuthProviderEnv": "prod",
		})
		return
	}

	ku := s.userForToken(in.Token)
	if ku == "" {
		log.Printf("[klei] TokenPurpose: token not one of ours: %s", summarise(body))
		loginError(w, "E_WRONG_TOKEN_PURPOSE")
		return
	}

	name := s.names.get(ku)
	log.Printf("[klei] TokenPurpose: %s (%s)", ku, name)

	// Two different client actions POST to this one path, and which one is running is the
	// client's own state — the request body is `{"Token":…}` either way. So the reply carries
	// both sets of fields, and the action reads the half it wants:
	//
	//	TokenPurpose          Game, UserID, Username, AnalyticsJWT   (nso 0x21e2d4)
	//	GenerateClusterToken  Token, and nothing else                (nso 0x21df2c)
	//
	// Answering only the first is why hosting still failed after TokenPurpose started working:
	// the second call went down "[Error] No cluster_token received" and the server never
	// started, with no syscall behind the failure because the game had not reached the network
	// yet. The cluster token is the session token again — opaque to the client, and the lobby
	// then recognises the broadcast as this player's.
	writeJSON(w, 200, map[string]any{
		"Error":           false,
		"Token":           s.mintToken(ku),
		"Game":            dstGame,
		"UserID":          ku,
		"Username":        name,
		"AnalyticsJWT":    "",
		"AuthProviderEnv": "prod",
	})
}

// gameSessionToken clears a joining player for one specific server.
//
// This is the last thing between a host and its own world: the moment the client goes to join
// the server it just started, it POSTs {"Token":…,"Server":"KU_…"} here. Unanswered, it puts up
// "Could not connect, unable to reach the Klei server" and the server shuts back down — which is
// exactly what a 404 from this path looked like on 2026-09-29.
//
// The client reads a token string out of the reply and refuses an empty one
// ("[Connect] Received an empty GameSessionToken.", nso 0x1b8d40) or one it considers stale
// ("[Connect] Received a GameSessionToken that is no longer valid.", nso 0x1b8d0c). It then
// hands that token to the server, which posts it back to TokenPurpose to be checked.
//
// ponytail: the reply carries the token under both "Token" and "GameSessionToken". Which name
// the client reads is not recovered yet — the parser takes the string from its caller — and one
// spare key costs nothing next to another round trip through the console. Drop the other once a
// run shows which one it took.
func (s *server) gameSessionToken(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var in struct {
		Token  string `json:"Token"`
		Server string `json:"Server"`
	}
	_ = json.Unmarshal(body, &in)

	ku := s.userForToken(in.Token)
	if ku == "" {
		log.Printf("[klei] GameSessionToken: token not one of ours: %s", summarise(body))
		loginError(w, "E_INVALID_CLIENT_TOKEN")
		return
	}
	if in.Server == "" {
		log.Printf("[klei] GameSessionToken: no Server in %s", summarise(body))
		loginError(w, "E_NO_ID")
		return
	}

	// The client does not just read the token: in WAIT_TOKEN it compares a string from this
	// reply against the server it is dialling, and a mismatch is "Received a GameSessionToken
	// that is no longer valid" (nso 0x1b8cb4 -> 0x1b8d0c) — which is what an answer without it
	// looked like on 2026-09-29. That string is the server's RakNet guid, and the request names
	// the host only by UserID, so it comes from the listing that host broadcast.
	guid := s.lobbies.guidForHost(in.Server)
	if guid == "" {
		log.Printf("[klei] GameSessionToken: no live listing for server %s; answering without a guid", in.Server)
	}

	tok := s.mintGameSession(ku, in.Server)
	log.Printf("[klei] GameSessionToken: %s cleared for server %s guid=%s", ku, in.Server, guid)
	writeJSON(w, 200, map[string]any{
		"Error":            false,
		"Token":            tok,
		"GameSessionToken": tok,
		"GUID":             guid,
		"Server":           in.Server,
		"UserID":           ku,
		"Username":         s.names.get(ku),
	})
}

// mintGameSession binds the joining player to the host they were cleared for. Same trick as
// mintToken: the pair is inside the HMAC, so the validate path can name the host again without a
// session table, and a token cannot be replayed against a different one.
func (s *server) mintGameSession(ku, host string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("gamesession\x00" + ku + "\x00" + host))
	return ku + "." + host + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:18])
}

// gameSessionParts returns the player and host a game session token names, or "","" if it was
// not minted here.
func (s *server) gameSessionParts(tok string) (ku, host string) {
	ku, rest, ok := strings.Cut(tok, ".")
	if !ok {
		return "", ""
	}
	host, _, ok = strings.Cut(rest, ".")
	if !ok {
		return "", ""
	}
	if !hmac.Equal([]byte(tok), []byte(s.mintGameSession(ku, host))) {
		return "", ""
	}
	return ku, host
}

// dstGame is the only game served here. The client checks it by name.
const dstGame = "DontStarveTogether"

// loginError answers the shape the client's failure branch reads: "Error" as a string (which is
// exactly what makes it *not* a boolean, and so a failure) plus the code it logs and shows.
func loginError(w http.ResponseWriter, code string) {
	writeJSON(w, 200, map[string]any{"Error": "true", "ErrorCode": code})
}

// kuid is the player's Klei user id, derived from their BAAS user id so it is the same on every
// login without storing anything. Klei's own are "KU_" and eight characters of [A-Za-z0-9_-],
// which is exactly six bytes of base64url.
func kuid(baasUserID string) string {
	sum := sha256.Sum256([]byte("openpak-klei-user\x00" + baasUserID))
	return "KU_" + base64.RawURLEncoding.EncodeToString(sum[:6])
}

// mintToken is the session token the client holds and hands to the lobby. It is an HMAC of the
// user id, so any handler can check one without a session table.
func (s *server) mintToken(ku string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(ku))
	// '.' separates them because neither half can contain one: both are base64url.
	return ku + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:18])
}

// userForToken returns the user a token names, or "" if it was not minted here.
func (s *server) userForToken(tok string) string {
	ku, _, ok := strings.Cut(tok, ".")
	if !ok || !hmac.Equal([]byte(tok), []byte(s.mintToken(ku))) {
		return ""
	}
	return ku
}

// summarise keeps a log line readable when the body is two JWTs.
func summarise(body []byte) string {
	if len(body) > 400 {
		return string(body[:400]) + "…"
	}
	return string(body)
}

func present(s string) string {
	if s == "" {
		return "absent"
	}
	return "present"
}
