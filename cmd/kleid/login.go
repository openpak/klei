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

	ku := s.userForToken(in.Token)
	if ku == "" {
		log.Printf("[klei] TokenPurpose: token not one of ours: %s", summarise(body))
		loginError(w, "E_WRONG_TOKEN_PURPOSE")
		return
	}

	name := s.names.get(ku)
	log.Printf("[klei] TokenPurpose: %s (%s)", ku, name)

	writeJSON(w, 200, map[string]any{
		"Error":           false,
		"Game":            dstGame,
		"UserID":          ku,
		"Username":        name,
		"AnalyticsJWT":    "",
		"AuthProviderEnv": "prod",
	})
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
