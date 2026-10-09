package main

// lobby-v2 — the server browser.
//
// A DST host broadcasts its game to /lobby/write and keeps it alive; everyone else downloads the
// listings and picks one. The game then connects host-to-player directly over UDP, so nothing in
// this file is on the gameplay path: it is an address book that has to expire entries.
//
// The listing itself is the client's own JSON object (name, season, mods, connected, __addr,
// port, …, the forty-odd fields cServerListing serialises). We store it verbatim and hand it
// back unchanged. That is not laziness about the schema — the browser's fields come from the
// host's build, and interpreting them here would only let this service disagree with the game
// about what a listing says.
//
// The response shapes are read out of the Switch build (docs/protocol.md §3):
//
//	write/update/keepAlive         {"POST":{"__rowId":…,"__addr":…},"result":"OK","region":"<region>"}
//	                               — the client compares result to "OK" literally, takes its
//	                               row id from POST, and logs every key it does not know
//	delete                         {"result":"OK","region":"<region>"}
//	read                           {"GET":[<listing>,…]} — "error" instead when it failed
//	getIP                          {"__addr":"<the caller's address>"}
//	regioncapabilities-v2.json     {"LobbyRegions":[{"Region":"<region>"}]}

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// region is the single lobby region OpenPak runs. The client turns it into the hostname it
// broadcasts to — "lobby-v2-" + region + ".klei.com" — so adding a second one means adding that
// name to Traefik and the network profile, not just this constant.
const region = "us-east-1"

// listingTTL drops a host that stopped keeping its listing alive. The game keeps alive well
// inside a minute; three is long enough to survive a hiccup and short enough that a closed game
// leaves the browser promptly.
const listingTTL = 3 * time.Minute

type listing struct {
	data     map[string]any // the host's own listing object, verbatim
	platform string
	seen     time.Time
}

type lobbyStore struct {
	mu   sync.Mutex
	rows map[string]listing
}

func (s *lobbyStore) put(rowID, platform string, data map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = map[string]listing{}
	}
	old, had := s.rows[rowID]
	if had && len(data) == 0 {
		// A keepAlive carries no listing: touch what is there rather than blanking it.
		old.seen = time.Now()
		s.rows[rowID] = old
		return
	}
	if had {
		// An update can be partial. Merge over what the host said last.
		merged := make(map[string]any, len(old.data)+len(data))
		for k, v := range old.data {
			merged[k] = v
		}
		for k, v := range data {
			merged[k] = v
		}
		data = merged
	}
	if platform == "" && had {
		platform = old.platform
	}
	s.rows[rowID] = listing{data: data, platform: platform, seen: time.Now()}
}

// guidForHost returns the RakNet guid of the live listing a given Klei user is hosting.
//
// The join handshake needs it: a client in WAIT_TOKEN compares the guid in the GameSessionToken
// reply against the server it is dialling, and a mismatch is "Received a GameSessionToken that
// is no longer valid" (nso 0x1b8d0c). The request only names the host by UserID, so the guid has
// to come from the listing that host broadcast — which is the row id, since a host brings its
// own guid (see newRowID).
func (s *lobbyStore) guidForHost(host string) string {
	if host == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, l := range s.rows {
		if time.Since(l.seen) > listingTTL {
			continue
		}
		if h, _ := l.data["host"].(string); h == host {
			if g, ok := l.data["guid"].(string); ok && g != "" {
				return g
			}
			return id
		}
	}
	return ""
}

func (s *lobbyStore) drop(rowID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, rowID)
}

// all returns the live listings, dropping expired ones on the way past. rowIDs, when not empty,
// narrows to those rows — that is how the client asks for one server's details.
func (s *lobbyStore) all(platform string, rowIDs []string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := map[string]bool{}
	for _, id := range rowIDs {
		want[id] = true
	}
	out := []map[string]any{}
	for id, l := range s.rows {
		if time.Since(l.seen) > listingTTL {
			delete(s.rows, id)
			continue
		}
		if len(want) > 0 && !want[id] {
			continue
		}
		if platform != "" && l.platform != "" && !strings.EqualFold(platform, l.platform) {
			continue
		}
		row := make(map[string]any, len(l.data)+1)
		for k, v := range l.data {
			row[k] = v
		}
		row["__rowId"] = id
		out = append(out, row)
	}
	return out
}

// lobby handles the /lobby/… paths, which arrive on lobby-v2-<region>.klei.com.
func (s *server) lobby(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var in map[string]any
	if len(body) > 0 && json.Unmarshal(body, &in) != nil {
		log.Printf("[klei] lobby %s: unreadable body (%d bytes)", r.URL.Path, len(body))
		writeJSON(w, 200, map[string]any{"Error": "true", "Code": "E_INVALID_INPUT"})
		return
	}
	rowID, _ := in["__rowId"].(string)
	gameID, _ := in["__gameId"].(string)
	tok, _ := in["__token"].(string)
	// ponytail: the token is logged, not enforced. The client's broadcast token comes from
	// /login/TokenPurpose, whose contract is not recovered yet, so refusing an unrecognised one
	// would refuse every host. Enforce once a real host has shown which token it sends.
	if tok != "" && s.userForToken(tok) == "" {
		log.Printf("[klei] lobby %s: token not one of ours (%.12s…)", r.URL.Path, tok)
	}

	switch r.URL.Path {
	case "/lobby/write", "/lobby/update", "/lobby/keepAlive":
		if rowID == "" {
			rowID = newRowID(in)
		}
		data := map[string]any{}
		for k, v := range in {
			// __addr is the one __ key that belongs to the listing rather than to the envelope:
			// it is the address a browsing player dials. Keep whatever the host sent.
			if k == "__addr" || (!strings.HasPrefix(k, "__") && k != "masterversion") {
				data[k] = v
			}
		}
		platform, _ := in["platform"].(string)
		s.lobbies.put(rowID, platform, data)
		log.Printf("[klei] lobby %s: game=%q row=%s fields=%d addr=%v port=%v nat=%v v=%v host=%v version=%q",
			r.URL.Path, gameID, rowID, len(data), in["__addr"], in["port"], in["nat"], in["v"], in["host"], s.versions.get(str(in["host"])))
		if r.URL.Path == "/lobby/write" {
			// A host writes its full listing once per start; that body is the only record of what
			// a Switch host actually broadcasts, and the join path is read out of it.
			log.Printf("[klei] lobby write listing: %s", body)
		}
		// The host takes its row id from POST.__rowId (nso 0x1cca44, 0x1fd2f0). A master shard
		// will not read its shard link at all until it holds both a row and a region (0x20ca78),
		// so without POST a world with caves never finishes launching: the secondary's info
		// packet sits unread and the player waits on "DST_Secondary Ready" for ever.
		writeJSON(w, 200, map[string]any{
			"POST":   map[string]any{"__rowId": rowID, "__addr": callerAddr(r)},
			"result": "OK", "region": region,
		})

	case "/lobby/getIP":
		// A secondary shard asks for its own address once the master has told it its row and
		// region, and reads exactly the top-level string __addr (nso 0x211790).
		log.Printf("[klei] lobby getIP: row=%s -> %s", rowID, callerAddr(r))
		writeJSON(w, 200, map[string]any{"__addr": callerAddr(r)})

	case "/lobby/delete":
		s.lobbies.drop(rowID)
		log.Printf("[klei] lobby delete: game=%q row=%s", gameID, rowID)
		writeJSON(w, 200, map[string]any{"result": "OK", "region": region})

	case "/lobby/read":
		rows := s.lobbies.all(queryString(in, "platform"), queryRows(in))
		reader := s.userForToken(tok)
		kept := rows[:0]
		for _, row := range rows {
			if host := str(row["host"]); !s.versions.fits(reader, host) {
				log.Printf("[klei] lobby read: row %v (host %s, version %s, v=%v) hidden from %s (version %s)",
					row["__rowId"], host, s.versions.get(host), row["v"], reader, s.versions.get(reader))
				continue
			}
			kept = append(kept, row)
		}
		rows = kept
		log.Printf("[klei] lobby read: game=%q reader=%s rows=%d", gameID, reader, len(rows))
		writeJSON(w, 200, map[string]any{"GET": rows})

	default:
		log.Printf("[klei] !! lobby %s %s not implemented, body=%s", r.Method, r.URL.Path, summarise(body))
		writeJSON(w, 200, map[string]any{"Error": "true", "Code": "E_INVALID_INPUT"})
	}
}

// callerAddr is the address the lobby saw the request come from.
// ponytail: behind Traefik's TCP passthrough that is Traefik's own 10.89.x address, not the
// console's. Both shards of a host get the same value, which is all the shard link needs; it
// becomes wrong for a remote player migrating shards. Fix with PROXY protocol on the klei router.
func callerAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// cdn serves lobby-v2-cdn.klei.com: the region list, and the pre-baked listing files the browser
// downloads instead of asking every region in turn.
func (s *server) cdn(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	switch {
	case path == "regioncapabilities-v2.json":
		writeJSON(w, 200, map[string]any{"LobbyRegions": []any{map[string]any{"Region": region}}})

	case strings.HasSuffix(path, ".json.gz"):
		// "<region>-<platform>.json.gz" — the platform is what keeps a Switch browser from
		// listing Steam hosts it could never reach.
		name := strings.TrimSuffix(path, ".json.gz")
		platform := ""
		if i := strings.LastIndexByte(name, '-'); i >= 0 {
			platform = name[i+1:]
		}
		rows := s.lobbies.all(platform, nil)
		log.Printf("[klei] cdn listings: %s rows=%d", path, len(rows))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		_ = json.NewEncoder(gz).Encode(map[string]any{"GET": rows})

	default:
		log.Printf("[klei] !! cdn %s %s not implemented", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

// queryRows pulls the row ids out of the read query, which carries either one id or a list.
func queryRows(in map[string]any) []string {
	q, _ := in["query"].(map[string]any)
	if q == nil {
		q = in
	}
	switch v := q["__rowId"].(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func queryString(in map[string]any, key string) string {
	if q, ok := in["query"].(map[string]any); ok {
		if s, ok := q[key].(string); ok {
			return s
		}
	}
	s, _ := in[key].(string)
	return s
}

// newRowID names a listing whose host did not bring its own id.
func newRowID(in map[string]any) string {
	if g, ok := in["guid"].(string); ok && g != "" {
		return g
	}
	return fmt.Sprintf("openpak-%d", time.Now().UnixNano())
}
