# Changelog — klei

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
