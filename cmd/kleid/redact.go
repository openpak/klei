package main

// What reaches the log.
//
// Everything this service is sent carries a credential or a person: session tokens, cluster
// tokens, the console's id tokens, Klei user ids, Nintendo netids, NEX session ids, and the
// player's and world's names. The bodies are still worth logging -- an unhandled request is the
// specification for its handler -- so they go through redactBody, which keeps every key and the
// length of every hidden value, and only drops what the value was. A string is hidden unless its
// key is one that only ever carries a fixed word (safeKeys); a number is hidden when its key names
// a person or a secret. Identifiers that log lines name directly go through who() or
// redact.Tag, a short per-process tag that still lets two lines about the same player be read
// together.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"openpak/klei/redact"
)

// secretKeys are the keys whose values never reach the log, matched case-insensitively and with
// any leading underscores ignored, so `__token`, `Token` and `token` are all one key.
var secretKeys = map[string]bool{
	// credentials
	"token": true, "clienttoken": true, "gamesessiontoken": true, "sessiontoken": true,
	"accounttoken": true, "nsatoken": true, "clustertoken": true, "cluster_token": true,
	"ticket": true, "password": true, "analyticsjwt": true, "id_token": true,
	"access_token": true, "refresh_token": true, "authorization": true, "secret": true,
	"code": true,
	// who someone is
	"userid": true, "user_id": true, "server": true, "host": true, "steamid": true,
	"ownernetid": true, "psnid": true, "netid": true, "id": true, "pid": true,
	"baas_user_id": true, "session": true, "steamroom": true, "steamclanid": true,
	"username": true, "nickname": true, "name": true, "desc": true, "players": true,
}

// moreSecretKeys are the keys a request this service has never seen might use for the same
// things: contact details, addresses, free text.
var moreSecretKeys = map[string]bool{
	"email": true, "mail": true, "ip": true, "addr": true, "address": true, "ipaddress": true,
	"ip_address": true, "displayname": true, "display_name": true, "nick": true,
	"owner": true, "friendcode": true, "friend_code": true, "pin": true, "cookie": true,
	"chat": true, "message": true, "msg": true, "text": true, "description": true,
	"worldname": true, "servername": true, "guid": true, "rowid": true, "lobbyid": true,
}

func isSecretKey(k string) bool {
	k = strings.ToLower(strings.TrimLeft(k, "_"))
	if safeKeys[k] {
		return false
	}
	if secretKeys[k] || moreSecretKeys[k] {
		return true
	}
	// Anything spelled like an id, a token or a name is one.
	return strings.HasSuffix(k, "id") || strings.HasSuffix(k, "ids") ||
		strings.Contains(k, "token") || strings.Contains(k, "name") || strings.Contains(k, "secret")
}

// safeKeys are the keys whose string values are a fixed vocabulary (a game, a platform, an
// event) and say nothing about a person. Every other string is hidden, so a request carrying a
// key nobody has met yet cannot put its text in the log.
var safeKeys = map[string]bool{
	"game": true, "gameid": true, "platform": true, "region": true, "intent": true, "mode": true,
	"event": true, "season": true, "version": true, "masterversion": true, "type": true,
	"kind": true, "action": true, "method": true, "state": true, "status": true,
}

func isSafeKey(k string) bool {
	k = strings.ToLower(strings.TrimLeft(k, "_"))
	return safeKeys[k]
}

// Free-text patterns that are secret wherever they turn up, including inside values a key does
// not give away: Klei user ids, JWTs, the tokens kleid mints, and Nintendo netids.
var secretText = regexp.MustCompile(
	`KU_[A-Za-z0-9_-]{8}(\.[A-Za-z0-9_.-]+)?` + // a Klei id, or a token minted from one
		`|eyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]*\.?[A-Za-z0-9_-]*` + // a JWT
		`|\bN:[A-Za-z0-9+/=_-]{8,}` + // a Nintendo netid as DST writes it
		`|\b7656119\d{10}\b`) // a SteamID64

func scrubText(s string) string {
	return secretText.ReplaceAllStringFunc(s, func(m string) string {
		return fmt.Sprintf("<redacted %d>", len(m))
	})
}

// redactBody renders a request or response body for the log: secret values replaced by their
// length, every key kept, and cut to a readable size.
func redactBody(body []byte) string {
	return cut(redacted(body), 400)
}

// redacted is redactBody without the cut, for the one body whose whole length is the point (a
// host's listing).
func redacted(body []byte) string {
	var s string
	var v any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	switch {
	case len(bytes.TrimSpace(body)) == 0:
		return ""
	case dec.Decode(&v) == nil:
		var out bytes.Buffer
		enc := json.NewEncoder(&out)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(redactValue("", v))
		s = strings.TrimSuffix(out.String(), "\n")
	default:
		if q, err := url.ParseQuery(string(body)); err == nil && len(q) > 0 && !bytes.ContainsAny(body, "{[ \n") {
			s = redactQuery(q)
		} else {
			// Not JSON and not a form: free text, whose content is nobody's business.
			s = fmt.Sprintf("<%d bytes of text>", len(body))
		}
	}
	return s
}

// cut keeps a log line readable when the body is a world's whole worldgen.
func cut(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// redactValue hides what a value under key k may not show. A string in an array inherits the
// array's key.
func redactValue(k string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for ck, e := range t {
			if isSecretKey(ck) {
				out[ck] = hidden(e)
			} else {
				out[ck] = redactValue(ck, e)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = redactValue(k, e)
		}
		return out
	case string:
		if !isSafeKey(k) {
			return hidden(t)
		}
		return scrubText(t)
	}
	return v
}

// hidden is what a secret value becomes. Booleans stay: `password: true` says a world has one,
// not what it is.
func hidden(v any) any {
	switch t := v.(type) {
	case bool, nil:
		return t
	case string:
		return fmt.Sprintf("<redacted %d>", len(t))
	}
	b, _ := json.Marshal(v)
	return fmt.Sprintf("<redacted %d>", len(b))
}

// redactQuery renders query parameters with the secret ones hidden.
func redactQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		for _, v := range q[k] {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			if isSecretKey(k) || !isSafeKey(k) {
				v = fmt.Sprintf("<redacted %d>", len(v))
			} else {
				v = scrubText(v)
			}
			b.WriteString(k + "=" + v)
		}
	}
	return b.String()
}

// who names a player or host in a log line without naming them: a per-process tag, so the
// lines about one player still read together.
func who(id string) string {
	if id == "" {
		return "-"
	}
	return redact.Tag(id)
}

// logGame is a game id as a log line may carry it: the game this service speaks for, or the
// length of whatever else the request called itself.
func logGame(g string) string {
	switch g {
	case "":
		return `""`
	case dstGame:
		return fmt.Sprintf("%q", g)
	}
	return fmt.Sprintf("<%d bytes>", len(g))
}

// logPath is a request path as a log line may carry it: scrubbed and cut.
func logPath(p string) string { return cut(scrubText(p), 80) }
