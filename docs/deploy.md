# Deploy: klei

Plain HTTP on **22640** inside the pod network (health on **22641**); Traefik terminates the
guest's TLS for these names, the same arrangement as [`servers/t17`](../../t17) and the Among Us
front.

## Running it

```
NX_INTERNAL_URL=http://127.0.0.1:20070 NX_INTERNAL_KEY=… go run ./cmd/kleid
```

`-addr` (22640), `-health-addr` (22641) and `-token-secret` are the knobs; without a token
secret the session tokens are valid for the life of the process, which is fine while one process
serves both tiers.

## Rewards sessions

Set `REWARDS_EVENTS_URL` (e.g. `http://openpak-rewards:20180`) and `REWARDS_EVENTS_KEY` (or `REWARDS_EVENTS_KEY_FILE`) to report
`game_sessions.dont-starve-together` events to the rewards service; unset, the reporter is off. `REWARDS_SESSION_MIN` (default `3m`) is how long a
player must share a room with at least one other player. A room is a lobby listing (live, written, host heard within 15 minutes); members come from the host's `players` roster, and a player is counted only if they have logged in through kleid at least once. The Klei-id to BAAS-id map is stored in Postgres (`klei_users`: `ku`, `baas_user_id`, `last_seen`) when `KLEI_DATABASE_URL` is set, upserted on every `LoginViaSwitch` and loaded at start; the pool is capped at 2 connections. Without it the map is memory only.
