package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	emit "github.com/openpak/rewards-emit"
)

type fakeRewards struct {
	mu     sync.Mutex
	events []map[string]any
}

func newFakeRewards(t *testing.T) (*fakeRewards, *emit.Client) {
	t.Helper()
	f := &fakeRewards{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body struct {
			Events []map[string]any `json:"events"`
		}
		_ = json.Unmarshal(b, &body)
		f.mu.Lock()
		f.events = append(f.events, body.Events...)
		f.mu.Unlock()
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	rw := emit.New(emit.Config{URL: srv.URL, Key: "k", Source: "klei-test", Flush: 10 * time.Millisecond})
	return f, rw
}

func (f *fakeRewards) drain(t *testing.T, rw *emit.Client) []map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rw.Close(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.events
}

func TestRosterKUs(t *testing.T) {
	got := rosterKUs(`return { {colour="#fff",name="a b",netid="KU_aaaaaaaa",prefab="wilson"}, {name="x",netid="KU_bbbbbbbb"}, {netid="KU_aaaaaaaa"}, {netid="N:foo"} }`)
	if len(got) != 2 || got[0] != "KU_aaaaaaaa" || got[1] != "KU_bbbbbbbb" {
		t.Fatalf("roster = %v", got)
	}
}

func TestSessionAfterThreeMinutesTogether(t *testing.T) {
	s := newServer(identityClient{}, "secret")
	a, b, c := kuid("aaaaaaaaaaaa0001"), kuid("bbbbbbbbbbbb0002"), kuid("cccccccccccc0003")
	s.baas.set(a, "aaaaaaaaaaaa0001")
	s.baas.set(b, "bbbbbbbbbbbb0002")
	// c never logged in through kleid, so it cannot be mapped and must not count.
	row := "row1"
	lobbyPost(t, s, "/lobby/write", `{"__rowId":"`+row+`","name":"w","platform":"Switch","host":"`+a+`","players":"return { {netid=\"`+a+`\"} }"}`)

	f, rw := newFakeRewards(t)
	sess := emit.NewSessions(rw, "game_sessions.dont-starve-together", 0)
	now := time.Now()
	tick := func(d time.Duration) int { return sess.Tick(now.Add(d), s.rewardRooms(now.Add(d))) }

	if tick(0) != 0 {
		t.Fatal("a lone host is not a session")
	}
	lobbyPost(t, s, "/lobby/update", `{"__rowId":"`+row+`","players":"return { {netid=\"`+a+`\"}, {netid=\"`+b+`\"}, {netid=\"`+c+`\"} }"}`)
	if n := tick(time.Minute); n != 0 {
		t.Fatalf("one minute together emitted %d", n)
	}
	if n := tick(4 * time.Minute); n != 2 {
		t.Fatalf("expected 2 events after 3 minutes together, got %d", n)
	}
	if n := tick(6 * time.Minute); n != 0 {
		t.Fatalf("events must not repeat, got %d", n)
	}
	ev := f.drain(t, rw)
	if len(ev) != 2 {
		t.Fatalf("rewards received %d events", len(ev))
	}
	for _, e := range ev {
		if e["metric"] != "game_sessions.dont-starve-together" {
			t.Errorf("metric %v", e["metric"])
		}
		if id, _ := e["event_id"].(string); !strings.Contains(id, "dst:row1@") {
			t.Errorf("event id %v lacks the room key", e["event_id"])
		}
		if idn, _ := e["identity"].(map[string]any); idn["namespace"] != "switch" {
			t.Errorf("identity %v", e["identity"])
		}
	}
}

func TestRewriteIsANewRoom(t *testing.T) {
	s := newServer(identityClient{}, "secret")
	lobbyPost(t, s, "/lobby/write", `{"__rowId":"r","name":"w","platform":"Switch"}`)
	first := s.rewardRooms(time.Now())[0].ID
	lobbyPost(t, s, "/lobby/update", `{"__rowId":"r"}`)
	if got := s.rewardRooms(time.Now())[0].ID; got != first {
		t.Fatalf("an update must keep the room: %s vs %s", got, first)
	}
	// The same guid written again an hour later is a new world.
	s.lobbies.mu.Lock()
	l := s.lobbies.rows["r"]
	l.created = l.created.Add(-time.Hour)
	s.lobbies.rows["r"] = l
	s.lobbies.mu.Unlock()
	older := s.rewardRooms(time.Now())[0].ID
	lobbyPost(t, s, "/lobby/write", `{"__rowId":"r","name":"w","platform":"Switch"}`)
	if again := s.rewardRooms(time.Now())[0].ID; again == older {
		t.Fatal("a rewrite must start a new room")
	}
}

func TestExpiredListingIsNoRoom(t *testing.T) {
	s := newServer(identityClient{}, "secret")
	lobbyPost(t, s, "/lobby/write", `{"__rowId":"r","name":"w","platform":"Switch"}`)
	if n := len(s.rewardRooms(time.Now().Add(listingTTL + time.Minute))); n != 0 {
		t.Fatalf("%d rooms from a silent host", n)
	}
}

type memBacking struct{ m map[string]string }

func (b *memBacking) upsert(_ context.Context, ku, baas string) error { b.m[ku] = baas; return nil }
func (b *memBacking) load(context.Context) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range b.m {
		out[k] = v
	}
	return out, nil
}

// A login is written through, and a fresh process that loads the store maps the roster again.
func TestKUMapSurvivesRestart(t *testing.T) {
	disk := &memBacking{m: map[string]string{}}
	s1 := newServer(stubIdentity(t, 200, `{"pid":1,"baas_user_id":"aaaaaaaaaaaa0001","nickname":"a"}`), "secret")
	if err := s1.baas.attach(context.Background(), disk); err != nil {
		t.Fatal(err)
	}
	out := post(t, s1, "/login/LoginViaSwitch", `{"NSAToken":"eyJ.a.b","Game":"DontStarveTogether"}`)
	ku, _ := out["UserID"].(string)
	if disk.m[ku] != "aaaaaaaaaaaa0001" {
		t.Fatalf("login was not persisted: %v", disk.m)
	}

	s2 := newServer(identityClient{}, "secret") // the restart: nobody has logged in yet
	if err := s2.baas.attach(context.Background(), disk); err != nil {
		t.Fatal(err)
	}
	lobbyPost(t, s2, "/lobby/write", `{"__rowId":"r","name":"w","platform":"Switch","players":"return { {netid=\"`+ku+`\"} }"}`)
	rooms := s2.rewardRooms(time.Now())
	if len(rooms) != 1 || len(rooms[0].Players) != 1 || rooms[0].Players[0].Subject != "aaaaaaaaaaaa0001" {
		t.Fatalf("roster did not map after restart: %+v", rooms)
	}
}
