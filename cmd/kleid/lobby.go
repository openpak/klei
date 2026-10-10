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
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// region is the single lobby region OpenPak runs. The client turns it into the hostname it
// broadcasts to — "lobby-v2-" + region + ".klei.com" — so adding a second one means adding that
// name to Traefik and the network profile, not just this constant.
const region = "us-east-1"

// listingTTL hides a host that stopped keeping its listing alive. It has to outlast the longest
// silence a live host keeps, and that is longer than it looks: a Switch host sends a keepAlive
// every eight minutes with an update of its day/players in between, so its gaps alternate ~3
// and ~5 minutes (measured 2026-09-30, keepAlives at 16:44:24, 16:52:11, 17:00:10), and once the
// world is paused with nobody in it the updates stop and only the keepAlives are left. The
// value up to v0.3.4 was three minutes: every host dropped out of the browser for part of each cycle.
const listingTTL = 15 * time.Minute

// listingKeep is how long a hidden listing is remembered. A host that comes back inside it --
// a late keepAlive, a long pause -- is revived whole, rather than rebuilt from the two or three
// fields its next update carries. That rebuild is what served the joiner a row of nothing but
// data/players/secondaries on 2026-10-10, which the browser drops ("0 of 0 shown").
const listingKeep = time.Hour

type listing struct {
	data     map[string]any // the host's own listing object, verbatim
	platform string
	seen     time.Time
	// written is whether a /lobby/write has given this row a whole listing. A row that only
	// ever saw update/keepAlive (a host that wrote before this process started) has no name,
	// version or port, and the browser cannot show it, so it is not served until it is written.
	written bool
}

type lobbyStore struct {
	mu   sync.Mutex
	rows map[string]listing
}

// write replaces a row with the whole listing a host broadcasts once per world start.
func (s *lobbyStore) write(rowID, platform string, data map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = map[string]listing{}
	}
	s.rows[rowID] = listing{data: data, platform: platform, seen: time.Now(), written: true}
}

// update merges a partial listing (update) or none at all (keepAlive) into a row and marks it
// alive. It reports whether the row had ever been written.
func (s *lobbyStore) update(rowID, platform string, data map[string]any) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = map[string]listing{}
	}
	old, had := s.rows[rowID]
	merged := make(map[string]any, len(old.data)+len(data))
	for k, v := range old.data {
		merged[k] = v
	}
	for k, v := range data {
		merged[k] = v
	}
	if platform == "" {
		platform = old.platform
	}
	s.rows[rowID] = listing{data: merged, platform: platform, seen: time.Now(), written: had && old.written}
	return had && old.written
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
		if time.Since(l.seen) > listingTTL || !l.written {
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
		if age := time.Since(l.seen); age > listingKeep {
			delete(s.rows, id)
			continue
		} else if age > listingTTL || !l.written {
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
	// UseNumber keeps every number exactly as the host wrote it (v, guid-sized ids), so the
	// listing goes back out byte-for-byte rather than through a float64.
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if len(body) > 0 && dec.Decode(&in) != nil {
		log.Printf("[klei] lobby %s: unreadable body (%d bytes)", r.URL.Path, len(body))
		writeJSON(w, 200, map[string]any{"Error": "true", "Code": "E_INVALID_INPUT"})
		return
	}
	rowID, _ := in["__rowId"].(string)
	gameID, _ := in["__gameId"].(string)
	tok, _ := in["__token"].(string)
	// ponytail: the token is noted, not enforced. The client's broadcast token comes from
	// /login/TokenPurpose, whose contract is not recovered yet, so refusing an unrecognised one
	// would refuse every host. Enforce once a real host has shown which token it sends.
	if tok != "" && s.userForToken(tok) == "" {
		log.Printf("[klei] lobby %s: token not one of ours (%d bytes)", r.URL.Path, len(tok))
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
		// write is the whole listing, once per world start, and replaces the row. update is a
		// partial listing (day, players) and keepAlive is none; both merge into the row the
		// write made and only mark it alive -- replacing it with what they carry would leave a
		// row the browser cannot show.
		written := true
		if r.URL.Path == "/lobby/write" {
			s.lobbies.write(rowID, platform, data)
		} else {
			written = s.lobbies.update(rowID, platform, data)
		}
		keys := "" // a write's keys are in its listing line below
		if r.URL.Path != "/lobby/write" {
			keys = " keys=" + keysOf(data)
		}
		log.Printf("[klei] lobby %s: game=%q row=%s fields=%d%s addr=%v port=%v nat=%v v=%v host=%s version=%q",
			r.URL.Path, gameID, rowID, len(data), keys, in["__addr"], in["port"], in["nat"], in["v"],
			who(str(in["host"])), s.versions.get(str(in["host"])))
		if !written {
			log.Printf("[klei] lobby %s: row %s has no listing from a write (host wrote before this process started?); hidden until the host writes again",
				r.URL.Path, rowID)
		}
		if r.URL.Path == "/lobby/write" {
			// A host writes its full listing once per start; that body is the only record of what
			// a Switch host actually broadcasts, and the join path is read out of it. Redacted:
			// the envelope carries the host's session token and the listing names the player.
			log.Printf("[klei] lobby write listing: %s", redacted(body))
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
					row["__rowId"], who(host), s.versions.get(host), row["v"], who(reader), s.versions.get(reader))
				continue
			}
			kept = append(kept, row)
		}
		rows = kept
		log.Printf("[klei] lobby read: game=%q reader=%s rows=%d", gameID, who(reader), len(rows))
		writeJSON(w, 200, map[string]any{"GET": rows})

	default:
		log.Printf("[klei] !! lobby %s %s not implemented, body=%s", r.Method, r.URL.Path, redactBody(body))
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
		log.Printf("[klei] cdn listings: %s rows=%d keys=%s", path, len(rows), rowKeys(rows))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		_ = json.NewEncoder(gz).Encode(map[string]any{"GET": rows})

	default:
		log.Printf("[klei] !! cdn %s %s not implemented", r.Method, scrubText(r.URL.Path))
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

// keysOf lists a listing's keys, sorted: what a host sent, without what it said.
func keysOf(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// rowKeys is the key count of each served row, which is enough to see a gutted one in the log.
func rowKeys(rows []map[string]any) string {
	n := make([]string, len(rows))
	for i, r := range rows {
		n[i] = fmt.Sprint(len(r))
	}
	return "[" + strings.Join(n, " ") + "]"
}
