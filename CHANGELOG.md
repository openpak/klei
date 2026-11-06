# Changelog — klei

## v0.8.2 -- 2026-11-06

- Card #150: every chest the game ever sold (481) is on sale in the Shop and the Rewards shops, under the game's own purchase rules. The generator writes `grants` and `requires` per chest, marks the six picture-less gift chests (Loyal) earned, and Heirloom skins earned; a test checks every chest expands to its contents. docs/protocol.md 4d.

## v0.8.1 -- 2026-11-05

- Card #141: the in-game Shop sells for points. `GetPricingInfo` lists Rewards' packs as `IAPDefs` in `BOLTS`, `GetAllItems` carries `CurrencyMap.BOLTS` = the player's points, `PurchaseWithCurrency` buys through Rewards (insufficient points `E_BALANCE_MISMATCH`, price/unsellable `E_PRICE_MISMATCH`), and owning a pack lists its items (`cmd/kleid/packs.json` from the generator, 487 packs). docs/protocol.md 4c. Only with `KLEI_ITEMS=rewards`.

## v0.8.0 -- 2026-11-05

- Card #141: `KLEI_ITEMS=rewards`. `GetAllItems` lists the player's DST skins from Rewards by network identity (events key), cached 60 s and refreshed on `entitlement_changed` from the account core stream; `KLEI_ITEMS=probe` and the test list stay for experiments. `tools/catalog/gen_catalog.py` + `dst-import.json`: the 431 character skins for the Rewards import. Equip is local to the console (docs/protocol.md 4c).

## v0.7.5 -- 2026-11-05

- Item step (card #141): with `KLEI_ITEMS_TEST=wilson_formal` the list now has the real skin key (`wilson_formal` is a character skin in the game's prefabskins.lua; `ItemID` stays a number, the item key a string) and pricing lists every test item at 999999 plus the `openpak_none` placeholder, so the pricing reply is never empty and no def is free. Variant 1 (empty list + `UserID`) passed on the owner's Switch: My Item Collection opens. The Shop says "The Klei store is closed right now" because the Shop screen shows that when no def survives its filter (in_store, a known MISC_ITEMS type, virtual currency): expected with nothing for sale.

## v0.7.4 -- 2026-11-05

- `GetPricingInfo` with no test item still lists one def (`openpak_none`, never shown by the Shop): an empty `IAPDefs` is "Missing IAPDefs" to the client, so the empty-inventory run had a failing pricing reply.

## v0.7.3 -- 2026-11-05

- `GetAllItems` carries `UserID`: the Klei user id the request's `ClientToken` names. The game's inventory processor compares it with the signed-in user's id and rejects the whole reply otherwise; the 19:28 console run (every call 200, no `UserID`) still showed "failed to download your item collection". Found in the disassembly (nso 0x188d50), not yet confirmed on a console. With `KLEI_ITEMS_TEST` unset the list is empty, which isolates the envelope from the items.

## v0.7.2 -- 2026-11-05

- `KLEI_ITEMS=probe` also answers `GET /iap/dst/GetShopEpoch` (`{"Time": now}`; the Shop warns when the clock is more than 60 s off) and `POST /iap/DST/PurchaseCreateAndComplete` (`{"Error": false, "Message": "Purchase Completed", "Items": []}`: nothing to settle, nothing is sold for money). Both seen in the owner's console run.

## v0.7.1 -- 2026-11-05

- docs/protocol.md 4b: why the in-game Shop hung on "Loading..." (the Nintendo shop-service consumable list, answered by nx-shop v0.3.1 with an empty list) and every call the Shop makes. No code change.

## v0.7.0 — 2026-11-05

- Card #141: the item replies now have the shape the client reads. The v0.6.0 probe sent `"Error": ""` (a string) and an empty `IAPDefs`; the client reads `Error` as a boolean that must be false, so the skins screen said "failed to download your item collection". `GetAllItems` answers `Error: false`, `Unchanged: false`, a state token and `Items` (`ItemID` number, `ItemType` string); `DailyGift` and `KeyValuesGetMulti` get proper empty replies; `GetPricingInfo` only with `KLEI_ITEMS_PRICING=on` (needs a non-empty `IAPDefs`). `KLEI_ITEMS_TEST` lists item types every account owns, for the test run. Still off by default.

## v0.6.0 — 2026-11-05

- Card #141 (Rewards unlocks game cosmetics): `KLEI_ITEMS=probe` answers `GetPricingInfo` and `GetAllItems` on `items.kleientertainment.com` with an empty, well-formed reply so a Harbor run can show how the client reacts. Off by default (the 404 stands). docs/protocol.md section 4a records the reply keys recovered from the Switch build.

## v0.5.0 — 2026-11-04

- Account erasure (prds#120): `DELETE /internal/accounts/{account_id}/data` on the health listener (22641; `Authorization: Bearer $ACCOUNT_ERASE_KEY`, unset = 503). Resolves the account's BAAS ids through the core (`ACCOUNT_GRPC_ADDR`, `ACCOUNT_INTERNAL_KEY`, read-only ListAccountLinks; without them the call fails 500 so the account is not marked erased) and removes the `klei_users` rows, the account's entries in `klei_session_clock`, and the in-memory names, versions and lobby listings that mention its Klei id. Idempotent, one `account erased` log line. No authored content is stored, so nothing is kept.

## v0.4.0 — 2026-11-03
- Per-request logging: one slog line per HTTP request (method, path, status, ms) on stderr via internal/httplog. Query strings, headers and bodies are never logged; /healthz and /metrics are skipped.

## v0.3.11 — 2026-11-03

**Rewards events survive a restart.** Bumps rewards-emit to v0.3.0: with `REWARDS_EMIT_DATABASE_URL` set the undelivered reward-event queue lives in Postgres (schema `emit`) instead of memory. Unset, behaviour is unchanged.

## v0.3.10 — 2026-11-03

**Rewards state is permanent.** The session clock (who has been in a room together since when, and which
players already counted) is saved to Postgres (`klei_session_clock`) after every tick and restored at start,
next to the Klei-id map, so a restart no longer resets the three-minute timer or lets a session count twice.
If the database is down kleid keeps running on memory, logs `ERROR` lines, keeps the unsaved id-map writes
pending and flushes them when the store is back (the connect is retried every 30 seconds; the clock is saved
again on the next tick). Lobby listings themselves are still in memory: after a restart a host's world only
reappears when it writes its listing again, and a rewritten world is a new room, so a restored clock only
helps a room that comes back under the same key.

## v0.3.9 — 2026-11-03

**The Klei-id map survives a restart.** The map from Klei user id to BAAS user id that rewards sessions
need is now kept in Postgres (`klei_users`, set `KLEI_DATABASE_URL`): upserted with a `last_seen` on every
`LoginViaSwitch`, loaded at start, two connections at most. Without the variable, or if the database is
down, kleid runs on the in-memory map as before.

## v0.3.7 — 2026-11-03

**Rewards sessions.** A player now earns a Don't Starve Together session (`game_sessions.dont-starve-together`)
after three unbroken minutes in a live lobby listing with another player. Membership is the only thing the
lobby observes, the host's `players` roster (Klei user ids), mapped to BAAS user ids remembered from
`LoginViaSwitch`; players the process did not log in are left out. A rewritten world is a new room. Off unless
`REWARDS_EVENTS_URL` and `REWARDS_EVENTS_KEY` are set. The image build now downloads modules.

## v0.3.6 — 2026-10-10

**The log carries nothing a person typed or a client's address.** The tags that name players and
hosts in log lines are now keyed per process (HMAC, new on every start) instead of an unkeyed
hash of the Klei id, which anyone could recompute; they correlate lines within one run and
nothing else. A listing's row id and RakNet guid are tagged too, and the lobby lines no longer
print the `__addr` a host wrote or the address `getIP` answered. Request bodies and query strings
now show a string only under the few keys that carry a fixed word (game, platform, event, ...);
every other string, and every id or name-like key, is logged as its length, and a body that is
neither JSON nor a form is logged as its size. Game ids a client invents, request paths and
errors from nx-baas (which spell out its URL) are cut down the same way.

## v0.3.5 — 2026-10-10

**A host's world stays in the browser.** A joiner's Browse Games showed "0 of 0 shown" while the
listing file served one row -- a row of nothing but `data`, `players` and `secondaries`, which
the browser drops. A listing expired three minutes after the host was last heard from, but a
Switch host is quiet for up to five minutes between its updates and keepAlives (longer once its
world is paused), so any browse in that window deleted the row, and the host's next keepAlive and
update rebuilt it from the two or three fields they carry. Listings now live 15 minutes, an
expired one is remembered for an hour and comes back whole, a write replaces the row while update
and keepAlive merge into it, and a row no write ever filled is not served. Numbers in a listing
go back out exactly as the host wrote them.

**The log no longer carries credentials or identities.** Request bodies (unhandled requests,
unimplemented login paths, refused tokens, the host's write listing) and query strings are
logged with every token, ticket, password, Klei id, netid, session id and name replaced by its
length, keys kept. Log lines name players and hosts by a short stable tag instead of their Klei
id, and the login line no longer logs the BAAS id, pid or nickname. A lobby update or keepAlive
logs which keys it carried, and the listing-file line logs each served row's key count.

## v0.3.4 — 2026-10-09

**Versions never share a world.** A login records the player's title version from the NSA id
token (`nintendo.av`). `/lobby/read` hides a listing whose host is on another version from the
reader, and GameSessionToken refuses a join across versions with `E_WRONG_API_VERSION`, a code
the client carries. Writes log the listing's `v` beside its host's version. A player whose
version is unknown sees and joins everything, as before; the pre-baked `.json.gz` carries no
token and is not filtered.

## v0.3.2 — 2026-09-29

**TokenPurpose is where a game session token is validated, and that makes three actions on one
path.** The server a player joins posts the player's game session token back to
`/login/TokenPurpose` -- there is no `/login/ValidateToken`, which was a guess. Unrecognised, it
answered E_WRONG_TOKEN_PURPOSE and the player got "Authentication failed". It now recognises a
game session token and replies with the `Server` that token was minted for, which is what the
server compares against its own id.

## v0.3.1 — 2026-09-29

**The GameSessionToken reply has to name the server's guid.** With v0.3.0 the client got its
token and then threw it away: "[Connect] Received a GameSessionToken that is no longer valid."
In WAIT_TOKEN it compares a string out of the reply against the server it is dialling, and a
missing one fails that compare. The string is the server's RakNet guid; the request names the
host only by UserID, so the guid is looked up from the listing that host broadcast to the lobby.

## v0.3.0 — 2026-09-29

**Serve GameSessionToken and ValidateToken — the last step before a host reaches its own world.**
A 404 on `/login/GameSessionToken` is what put "Could not connect, unable to reach the Klei
server" on screen: the moment the client goes to join the server it just started, it asks Klei
to clear it for that host, and without an answer the server shuts back down. The client then
hands the token to the server, which calls `ValidateToken` and compares the `Server` it gets
back to its own id — so the host is inside the HMAC and a token cannot be replayed against a
different one.

## v0.2.2 — 2026-09-29

**Keep `__addr`, and log what a host actually broadcasts.** The write handler stripped every
`__`-prefixed key as envelope, `__addr` with them — and `__addr` is the address a browsing
player dials, so a listing came back out of the browser with nowhere to connect. It is now
carried through. `/lobby/write` also logs the full listing once per host start (keepAlives stay
quiet): hosting has worked since 2026-09-28 but nothing has ever browsed, and that body is what
says whether a joining console gets an address.

## v0.2.1 — 2026-09-28

**TokenPurpose also has to hand back a cluster token.** A host calls that one path twice and the
two calls are *different actions* — the second is `GenerateClusterToken`, which reads `Token`
and nothing else. Answering only the identity half left the host on "[Error] No cluster_token
received" and still "Server failed to start", with nothing in the emulator log because the abort
comes before the game touches the network. The reply now carries both halves.

## v0.2.0 — 2026-09-27

**A Switch host can start its server.** `/login/TokenPurpose` is what a host asks before it
starts — twice, once per shard — and an unanswered one is "Server failed to start" followed by a
disconnect, which is exactly how far the first hosting attempt got. It now answers with the
identity the token stands for, including the game name the client checks by hand.

Also recorded, not served: the item server's four opening requests, captured verbatim now that
an unhandled request logs its body. The client survives their 404s, so skins and inventory stay
unserved and the browser comes first.

## v0.1.1 — 2026-09-27

An unhandled request now logs its **body**, not just its method and path. The first Switch run
past login went straight to `items.kleientertainment.com` (skins and inventory), and a line that
says only "GET /clientitems/DST/ReportMission/" is not enough to write the handler from. The
request is the specification, and a guest rarely asks twice.

## v0.1.0 — 2026-09-27

**Don't Starve Together can log in.** The title's whole online stack is Klei's own HTTPS REST —
no NEX, no NPLN, no Photon — and the one request that fails is `POST /login/LoginViaSwitch`
against real Klei, which answers HTTP 500 because the NSA token in it was signed by OpenPak and
Nintendo cannot vouch for it. `kleid` answers that request from nx-baas's own view of the
account: the console's BAAS id_token in, a stable `KU_xxxxxxxx` Klei user and a session token
out, with the player's nickname as their Klei username.

Also here, built to the same recovered contract but not yet exercised by a real client: the
lobby-v2 server browser (`/lobby/write|update|keepAlive|delete|read`, the region list and the
gzipped per-platform listing files). The host's listing object is stored verbatim and handed
back unchanged.

The wire contract was read out of the Switch build's own executable rather than guessed —
[`docs/protocol.md`](docs/protocol.md) records every address it came from. The load-bearing
detail: on success `Error` must be the JSON **boolean** `false`, because the client decides
success from that member's *type*, and Klei's own failures are the string `"true"`.
