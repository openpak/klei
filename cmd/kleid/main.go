// Command kleid serves Klei Entertainment's own online stack for the Switch titles that use it —
// today Don't Starve Together (010090100E334000).
//
// DST reaches no Nintendo online service at all beyond the BAAS account it is already logged into.
// Everything else is Klei's own HTTPS REST, on three tiers:
//
//	login.kleientertainment.com   /login/LoginViaSwitch — the console's BAAS id_token in, a Klei
//	                              user (KU_xxxxxxxx) and an opaque session token back
//	lobby-v2-cdn.klei.com         the region list and the per-region, per-platform server listings
//	lobby-v2-<region>.klei.com    /lobby/read|write|update|keepAlive|delete — the server browser
//
// Gameplay itself never comes here: DST connects the joining player straight to the host over UDP.
//
// Plain HTTP inside the pod network; Traefik terminates the guest's TLS for these names and
// forwards the stream, the same arrangement as servers/t17 and the Among Us front.
//
// The wire contract was read out of the Switch build's own executable (v1.29.0) rather than
// guessed — see docs/protocol.md, and keep that file in step with this code.
package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

var (
	addr        = flag.String("addr", ":22640", "HTTP listen address (Traefik terminates TLS in front)")
	healthAddr  = flag.String("health-addr", ":22641", "health listen address")
	tokenSecret = flag.String("token-secret", os.Getenv("KLEI_TOKEN_SECRET"),
		"HMAC secret the session token is derived with; empty means one random per boot")
)

func main() {
	flag.Parse()
	s := newServer(newIdentityClient(), *tokenSecret)

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.route)
	go func() {
		h := http.NewServeMux()
		h.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, 200, map[string]any{"status": "ok"})
		})
		log.Printf("[klei] health on %s", *healthAddr)
		log.Fatal(http.ListenAndServe(*healthAddr, h))
	}()

	log.Printf("[klei] serving login and lobby-v2 on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

type server struct {
	ids      identityClient
	secret   []byte
	names    nameStore
	versions versionStore
	lobbies  lobbyStore
}

// route dispatches on path first and hostname second. The three tiers arrive on one port behind
// Traefik, and their paths do not collide, so the host only has to separate the two lobby tiers.
func (s *server) route(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/login/"):
		s.login(w, r)
	case strings.HasPrefix(r.URL.Path, "/lobby/"):
		s.lobby(w, r)
	case strings.HasPrefix(host, "lobby-v2-cdn."):
		s.cdn(w, r)
	default:
		// Every unhandled request is a finding: this stack was recovered from one title, and the
		// next name or path a guest asks for is how the next one gets added. Log the body too --
		// the request is the specification for the handler that has to answer it, and a guest
		// rarely asks twice.
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		log.Printf("[klei] !! unhandled %s %s%s body=%s", r.Method, host, r.URL.Path, summarise(body))
		writeJSON(w, 404, map[string]any{"error": "not found"})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
