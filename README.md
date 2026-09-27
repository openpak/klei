# klei — OpenPak's Klei Entertainment backend

The service a Klei Switch title reaches when a console is redirected to OpenPak. Today that is
**Don't Starve Together** (`010090100E334000`). One process serves both tiers, dispatching on
path and hostname:

| Host | What it answers |
| --- | --- |
| `login.kleientertainment.com` | `POST /login/LoginViaSwitch` — the console's own BAAS token in, a Klei user out |
| `lobby-v2-cdn.klei.com` | the region list and the pre-baked `<region>-Switch.json.gz` listings |
| `lobby-v2-us-east-1.klei.com` | `/lobby/write\|update\|keepAlive\|delete\|read` — the live server browser |

Plain HTTP on **22640** inside the pod network (health on **22641**); Traefik terminates the
guest's TLS for these names, the same arrangement as [`servers/t17`](../t17) and the Among Us
front.

`galette.klei.com` and `cdn-galette.klei.com` are deliberately **not** redirected: they serve the
game's own content and they work against real Klei.

## Status: login is built, the browser is built, neither is console-verified

DST's failure today is one request. `POST /login/LoginViaSwitch` carries the console's NSA
id_token, real Klei cannot have Nintendo vouch for a token OpenPak signed, and it answers HTTP
500 — which is the "connection to Klei failed" on screen (measured 2026-09-27, Ryujinx SSL
trace). This service answers it instead.

Nothing above that has been exercised by a real client yet. The lobby routes are built to the
contract in [`docs/protocol.md`](docs/protocol.md) and log every request in full, so the first
host run says what the remaining gaps are. Known gaps are listed in §4 of that file — the
nearest one is `/login/TokenPurpose`, which is how a host gets its broadcast token.

## Why the console's token is not verified here

`LoginViaSwitch` carries a BAAS `id_token` that **OpenPak signed**. This service could check the
signature itself, the way `servers/t17` does — it asks nx-baas's `/internal/switch/identity`
instead, because that answers three things a signature cannot: the player's nickname (which
becomes their Klei `Username`), whether the account is banned, and "this player is online now",
which is what the titles grid counts playtime from.

## The listing is the host's, not ours

A DST server listing has forty-odd fields and they come from the host's build. `kleid` stores
the object the host wrote and hands it back unchanged; it adds `__rowId` and interprets nothing
else. Two builds then agree with each other rather than with us.

## Running it

```
NX_INTERNAL_URL=http://127.0.0.1:20070 NX_INTERNAL_KEY=… go run ./cmd/kleid
```

`-addr` (22640), `-health-addr` (22641) and `-token-secret` are the knobs; without a token
secret the session tokens are valid for the life of the process, which is fine while one process
serves both tiers.
