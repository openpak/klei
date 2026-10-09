package main

// Versions never share a world.
//
// A DST host's listing carries its build's network version `v`, and the browser and the join
// path are where two builds would meet. The id_token a player logs in with (NSAToken, minted by
// nx-baas) carries nintendo.av, the installed title version (%08x), which for one platform names
// the build just as `v` does, and is known for every player who logged in, browsing or hosting.
// So a listing is filtered by its host's title version against the reader's, and a join token
// is not issued across versions. `v` is logged beside it, so the two can be read together.
//
// Only what can be read decides: a player whose version is unknown (no claim, not 8 hex digits,
// or a login from before a restart) sees and joins everything, as before. The pre-baked
// .json.gz file carries no token, so it cannot be filtered per reader; /lobby/read (the details
// the browser asks before a join) and GameSessionToken can.

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
)

// appVersionClaim reads nintendo.av out of an NSA id token's payload; nx-baas proves the token.
func appVersionClaim(idToken string) string {
	seg := strings.Split(idToken, ".")
	if len(seg) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(seg[1], "="))
	if err != nil {
		return ""
	}
	var c struct {
		Nintendo struct {
			AV string `json:"av"`
		} `json:"nintendo"`
	}
	if json.Unmarshal(payload, &c) != nil || !isAppVersion(c.Nintendo.AV) {
		return ""
	}
	return strings.ToLower(c.Nintendo.AV)
}

func isAppVersion(av string) bool {
	if len(av) != 8 {
		return false
	}
	for _, c := range av {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// versionStore is each Klei user's title version as of their last login.
type versionStore struct {
	mu sync.Mutex
	by map[string]string
}

func (v *versionStore) set(ku, av string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.by == nil {
		v.by = map[string]string{}
	}
	if av == "" {
		delete(v.by, ku)
		return
	}
	v.by[ku] = av
}

func (v *versionStore) get(ku string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.by[ku]
}

// fits: same version, or either side unknown.
func (v *versionStore) fits(a, b string) bool {
	va, vb := v.get(a), v.get(b)
	return va == "" || vb == "" || va == vb
}
