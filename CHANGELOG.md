# Changelog — klei

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
