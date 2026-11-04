package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	emit "github.com/openpak/rewards-emit"
)

type fakeLinks map[string][]string

func (f fakeLinks) SwitchSubjects(_ context.Context, a string) ([]string, error) { return f[a], nil }

// eraseBaas on the in-memory backing mirrors the SQL: drop the map rows and scrub the clock.
func (b *memBacking) eraseBaas(_ context.Context, baas []string) (int, error) {
	n := 0
	for k, v := range b.m {
		for _, x := range baas {
			if v == x {
				delete(b.m, k)
				n++
			}
		}
	}
	if out, removed, ok := scrubClock(b.clock, baas); ok {
		b.clock, n = out, n+removed
	}
	return n, nil
}

const (
	acctA = "11111111-1111-4111-8111-111111111111"
	acctB = "22222222-2222-4222-8222-222222222222"
)

func TestEraseRemovesOnlyThatAccount(t *testing.T) {
	st := emit.State{Since: map[string]time.Time{
		"room\x00switch\x00baasA": time.Now(), "room\x00switch\x00baasB": time.Now()},
		Emitted: []string{"room\x00switch\x00baasA", "room\x00switch\x00baasB"}}
	clock, _ := json.Marshal(st)
	kuA, kuB := kuid("baasA"), kuid("baasB")
	disk := &memBacking{m: map[string]string{kuA: "baasA", kuB: "baasB"}, clock: clock}
	s := newServer(identityClient{}, "secret")
	s.links = fakeLinks{acctA: {"baasA"}, acctB: {"baasB"}}
	if err := s.connect(context.Background(), disk, emit.NewSessions(nil, sessionMetric, 0), true); err != nil {
		t.Fatal(err)
	}
	s.names.set(kuA, "a")
	s.names.set(kuB, "b")
	s.versions.set(kuA, "1")
	s.lobbies.write("row1", "switch", map[string]any{"players": `{netid="` + kuA + `"}`})
	s.lobbies.write("row2", "switch", map[string]any{"players": `{netid="` + kuB + `"}`})

	n, err := s.Erase(context.Background(), acctA)
	if err != nil || n != 3 {
		t.Fatalf("erase: %d %v", n, err)
	}
	if _, ok := disk.m[kuA]; ok || disk.m[kuB] != "baasB" {
		t.Fatalf("map: %v", disk.m)
	}
	if strings.Contains(string(disk.clock), "baasA") || !strings.Contains(string(disk.clock), "baasB") {
		t.Fatalf("clock: %q", disk.clock)
	}
	if s.baas.get(kuA) != "" || s.baas.get(kuB) != "baasB" || s.names.get(kuA) != kuA || s.names.get(kuB) != "b" {
		t.Fatal("memory not scrubbed correctly")
	}
	if _, ok := s.lobbies.rows["row1"]; ok {
		t.Fatal("listing of the erased player kept")
	}
	if _, ok := s.lobbies.rows["row2"]; !ok {
		t.Fatal("other listing removed")
	}
	if n, err := s.Erase(context.Background(), acctA); err != nil || n != 0 {
		t.Fatalf("second erase: %d %v", n, err)
	}
}

func TestEraseNeedsCore(t *testing.T) {
	s := newServer(identityClient{}, "secret")
	if _, err := s.Erase(context.Background(), acctA); err == nil {
		t.Fatal("erase without a core must fail")
	}
}

func TestEraseEndpoint(t *testing.T) {
	s := newServer(identityClient{}, "secret")
	s.links = fakeLinks{}
	h := eraseHandler("klei", s.Erase)
	call := func(id, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("DELETE", "/internal/accounts/"+id+"/data", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	t.Setenv("ACCOUNT_ERASE_KEY", "")
	if r := call(acctA, "Bearer k"); r.Code != http.StatusServiceUnavailable {
		t.Fatalf("unset key: %d", r.Code)
	}
	t.Setenv("ACCOUNT_ERASE_KEY", "k")
	if r := call(acctA, ""); r.Code != 401 {
		t.Fatalf("no key: %d", r.Code)
	}
	if r := call(acctA, "Bearer bad"); r.Code != 401 {
		t.Fatalf("wrong key: %d", r.Code)
	}
	if r := call("nope", "Bearer k"); r.Code != 400 {
		t.Fatalf("bad id: %d", r.Code)
	}
	r := call(acctA, "Bearer k")
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"service":"klei"`) || !strings.Contains(r.Body.String(), `"rows":0`) {
		t.Fatalf("ok: %d %s", r.Code, r.Body.String())
	}
}
