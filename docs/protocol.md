# Klei's online stack, as the Switch build speaks it

Recovered 2026-09-27 from Don't Starve Together for Switch — `010090100E334000`, update v1.29.0,
build `DontStarveTogether/740510032` — two ways:

- a **live run** on Ryujinx with `RYU_SSL_TRACE=1`, which gives the hostnames, paths and the
  first 100 bytes of every request and reply;
- the title's **own executable** (`main.nso`, uncompressed, ARM64), which gives the rest: the
  JSON keys each handler reads and the branch that decides success from failure. Everything
  below marked *(nso …)* comes from a disassembly at that address.

Nothing here is guessed from the PC version or from community tooling. Where a shape is still
unknown it says so.

## 1. The tiers

DST reaches no Nintendo online service beyond the BAAS account the console is already signed
into. Its URL table *(nso 0x1fa800–0x1faa10)* is:

| Host | What it is | OpenPak |
| --- | --- | --- |
| `login.kleientertainment.com` | Klei accounts | **served** |
| `lobby-v2-cdn.klei.com` | region list and the pre-baked server listings | **served** |
| `lobby-v2-<region>.klei.com` | the live server browser (`/lobby/*`) | **served** |
| `lobby-v2.klei.com` | the region-less lobby name | not served; not yet observed in a run |
| `galette.klei.com`, `cdn-galette.klei.com` | content / patch delivery | left on real Klei — it works, and it serves the game's own data |
| `items.kleientertainment.com` | skins and inventory | not served |
| `clientservice.kleientertainment.com`, `*.gsm.events.klei.ca`, `crashes.klei.ca` | analytics and crash reports | not served, deliberately |

Gameplay is **not** on this list: once a player picks a server the game connects to the host
directly over UDP. Nothing in this service is on the gameplay path.

## 2. Login

```
POST https://login.kleientertainment.com/login/LoginViaSwitch
{"NSAToken":"<jwt>","AccountToken":"<jwt>","Game":…}
```

`NSAToken` is the console's `nn::account` NetworkServiceAccount id_token; on an OpenPak console
nx-baas signed it. `AccountToken` is the Nintendo Account authorization token the game fetches
alongside it *(nso 0x21c0bc–0x21c164: `NSAToken`, `AccountToken`, `Game`,
`FriendlyHostDescriptor`, `TokenExpiry`)*.

Real Klei answers an OpenPak console **HTTP 500** here, measured 2026-09-27: the token is
well-formed but Nintendo cannot vouch for it. That 500 is the "connection to Klei failed" the
player sees.

### The reply, and the one thing that must be right

The client looks for a member named `Error`, and follows the success path **only when its value
is a JSON boolean** *(nso 0x21d2cc: find `Error`; 0x21d320 `cmp w8,#6` on the value's type tag,
and `b.ne` to the failure branch)*. Type 6 is boolean in this JSON library; a string `"true"` is
type 2 and lands on the failure branch, which is exactly how Klei's own errors are shaped
(`{"Error":"true","ErrorCode":"E_BALANCE_MISMATCH"}` is a literal in the binary). A reply with
no `Error` member at all is a failure too — the member search runs off the end into the same
branch.

So:

```jsonc
// success
{"Error": false,                 // a boolean. Not "false".
 "Token": "<opaque>",            // echoed back to the other tiers
 "UserID": "KU_xxxxxxxx",        // "KU_" + 8 of [A-Za-z0-9_-]
 "Username": "<display name>",
 "AnalyticsJWT": "",
 "AuthProviderEnv": "prod"}

// failure
{"Error": "true", "ErrorCode": "E_INVALID_CLIENT_TOKEN"}
```

The success handler reads `Token`, `UserID`, `Username`, `AnalyticsJWT` and `AuthProviderEnv`,
each as a string, each optional *(nso 0x21dbcc–0x21dd10, through the string-field helper at
0x21e530 which returns empty for a missing key)*. `INVALID_CLIENT_TOKEN` *(0x21dbbc)* is the
code it recognises for a refused token.

Error codes the binary carries: `E_INVALID_INPUT`, `E_UNAUTHORIZED`, `E_DB_FAIL`, `E_NOT_IN_DB`,
`E_NO_ID`, `E_NO_NAME`, `E_NO_PORT`, `E_NO_ROWID`, `E_NO_GAMEID`, `E_NO_PLATFORMID`,
`E_NO_MASTERROWID`, `E_ROWID_EXIST`, `E_BANNED_GAME`, `E_BANNED_GAME_NAME`, `E_BANNED_GAME_DESC`,
`E_WRONG_API_VERSION`, `E_WRONG_TOKEN_PURPOSE`, `E_UNKNOWN_STEAMID`, `E_ALREADY_USED`,
`E_INVALID_CODE`, `E_PRICE_MISMATCH`, `E_CURRENCY_MISMATCH`, `E_BALANCE_MISMATCH`,
`E_NINTENDO_ERROR`.

### TokenPurpose — what a host asks before it starts

```
POST /login/TokenPurpose
{"Token":"<the session token from LoginViaSwitch>"}
```

Measured 2026-09-27: a Switch host sends this **twice** while starting its server (once per
shard) and, without an answer, puts up "Server failed to start" and disconnects the player.

The reply is the same envelope as the login — `Error` must be the boolean `false` — carrying the
identity the token stands for:

```jsonc
{"Error": false,
 "Game": "DontStarveTogether",   // compared by name against the client's own
 "UserID": "KU_xxxxxxxx",
 "Username": "<display name>",
 "AnalyticsJWT": "",
 "AuthProviderEnv": "prod"}
```

The client reads `Game` first and, on a mismatch, logs "[ACCOUNT_ACTION_TOKEN_PURPOSE] Game
mismatch failure, expected %s, received %s" and stops; otherwise it logs "Received (%s) from
TokenPurpose" with the UserID and starts the server *(nso 0x21e2c8–0x21e3fc)*.

**A Switch host sends this twice, and the two calls are different actions.** Which one is
running is the client's own state; the request body is `{"Token":…}` either way. The second is
`GenerateClusterToken`, which reads **`Token`** out of the reply, logs "cluster_token: %s" and
writes `cluster_token.txt`, and takes "[Error] No cluster_token received" when the field is
missing *(nso 0x21df2c–0x21dfd0)*. Measured 2026-09-28: answering only the identity half still
ends in "Server failed to start", and because the abort happens before the game touches the
network there is no syscall behind it — the emulator log simply stops.

So the reply carries both halves. `Token` is opaque to the client; `kleid` reuses the session
token, which also lets the lobby recognise the broadcast as that player's.

### GameSessionToken and ValidateToken — joining a server, including your own

```
POST /login/GameSessionToken   {"Token":"<session token>","Server":"KU_xxxxxxxx"}
POST /login/ValidateToken      {"Token":"<game session token>"}
```

Measured 2026-09-29: this is the **last** step before a host reaches its own world. The instant
the client goes to join the server it just started, it asks for a game session token; a 404 here
is "Could not connect, unable to reach the Klei server" on screen and the server shuts back down.

The client reads a token string out of the reply and rejects an empty one ("[Connect] Received
an empty GameSessionToken.", *nso 0x1b8d40*) or a stale one ("[Connect] Received a
GameSessionToken that is no longer valid.", *nso 0x1b8d0c*). It hands that token to the server,
which posts it to `ValidateToken` and reads the owner back:

```jsonc
// GameSessionToken
{"Error": false, "Token": "<opaque>", "GameSessionToken": "<same>",
 "UserID": "KU_xxxxxxxx", "Username": "<display name>"}

// ValidateToken
{"Error": false, "Server": "KU_xxxxxxxx", "UserID": "KU_xxxxxxxx", "Username": "<name>"}
```

`Error` is the JSON **boolean** again — the validate parser checks the member's type with the
same `cmp w8,#6` as login *(nso 0x21fac4)*. It then reads `Server` as a string and compares it
to the server's own id; a mismatch logs "Failed to set game session token, session host
mismatch" *(nso 0x171ae4)* and the join is refused. So a token minted for one host must not
validate for another — `kleid` puts the pair inside the HMAC rather than keeping a session table.

Which key the client reads the token from is **not** recovered — the parser receives the string
from its caller — so `kleid` sends it under both `Token` and `GameSessionToken`.

### Other /login paths, not yet implemented

`/login/Login`, `/login/CreateAccount`, `/login/ChangePassword`, `/login/RecoverPassword`,
`/login/Logout`, `/login/LogoutAll`, `/login/RegisterGameKey`, `/login/ReportUser`,
`/login/auto/?Game=DontStarveTogether`.

## 3. Lobby v2

Region list, fetched first:

```
GET https://lobby-v2-cdn.klei.com/regioncapabilities-v2.json
{"LobbyRegions":[{"Region":"us-east-1"}]}
```

*(nso 0x1f94b4 `LobbyRegions`, 0x1f94bc `Region`; the failure log is "ERROR reading region
capabilities. %s")*. The client builds `https://lobby-v2-<Region>.klei.com` from each name, so a
region is also a hostname that has to exist in DNS, Traefik and the network profile.

Pre-baked listings, one file per region and platform:

```
GET https://lobby-v2-cdn.klei.com/<region>-<platform>.json.gz   → gzip of {"GET":[<listing>,…]}
```

Platform for this build is `Switch`.

Live browser, on `lobby-v2-<region>.klei.com`:

| Path | Body | Reply |
| --- | --- | --- |
| `/lobby/write` | `__token`, `__gameId`, `__rowId`, `masterversion` + the listing | `{"result":"OK","region":"<region>"}` |
| `/lobby/update` | same, partial | same |
| `/lobby/keepAlive` | `__token`, `__rowId` | same |
| `/lobby/delete` | `__token`, `__rowId` | same |
| `/lobby/read` | `__token`, `__gameId`, `query` (`__rowId`, `region`, …) | `{"GET":[<listing>,…]}` |
| `/lobby/getIP` | as read; used by shard migration | as read |

The client compares `result` to the literal `OK`, logs `region` as "Server registered via geo
DNS in %s", warns "unknown key %s in server broadcast response" for anything else, and reads
`Code` for the error code *(nso 0x1cc9b0–0x1ccf3c)*. A read failure is an `error` key instead of
`GET` *(0x1cae08)*.

### The listing object

The fields `cServerListing` serialises *(nso 0x1fc000–0x1fe200)*:

```
__addr __rowId allownewplayers clanonly clienthosted clientmodsoff connected data dedicated desc
event fo guid host id intent kleiofficial lanonly maxconnections mode mods mods_info name nat
ownernetid password platform players port psnid pvp season secondaries serverpaused session
steamclanid steamid steamroom tags tick worldgen
```

`kleid` does not interpret any of them. The host writes its listing, the browser reads it back
unchanged, and the two builds agree with each other rather than with us. The only field the
service adds is `__rowId` on the way out.

**What a Switch host actually broadcasts**, captured 2026-09-29 16:34 local from a live Eden host
(the whole body is in the workspace at `scratch/dst/switch-host-listing.json`):

```jsonc
{"allownewplayers": true, "clanonly": false, "clienthosted": true, "clientmodsoff": false,
 "connected": 0, "data": "return { day=43, dayselapsedinseason=7, daysleftinseason=13 }",
 "dedicated": true, "desc": "", "fo": false, "guid": "13285033608306768903",
 "host": "KU_ZUFa4usM", "intent": "survival", "lanonly": false, "masterversion": 1,
 "maxconnections": 4, "mode": "survival", "mods": false, "mods_info": [],
 "name": "AndroidTest's World", "nat": 5, "password": false,
 "platform": 32,                              // an INTEGER, not the string "Switch"
 "players": "return {  }", "port": 10999,     // the Master shard's game port
 "pvp": false, "season": "spring", "secondaries": {}, "serverpaused": false,
 "session": "1155BB968E8485EB",               // the NEX session a joiner attaches to
 "steamid": "N:C4arryabHW4BAAAAAAAAAAE",      // the console's Nintendo netid, "N:"-prefixed
 "steamroom": "680893", "tags": "WS:AA,english,survival", "tick": 15, "v": 740510500,
 "worldgen": "return {str=\"<base64 zlib>\"}"}
```

Three things this settles:

- **There is no `__addr`.** The host sends none, so on real Klei the lobby fills it from the
  source IP of the write. OpenPak cannot: Traefik's TCP router splices the stream and `kleid`
  only ever sees an internal 10.89.x.
- **A Switch joiner probably never needs one.** `steamid` is the console's netid and `session` is
  a NEX session id — the pair a Switch client would use to punch through to `port` rather than
  dialling an address. That puts the NEX half (game server **251BB200**) on the join path.
- **`platform` is the integer 32**, so `kleid`'s `in["platform"].(string)` fails and it stores
  `""`. That disables the platform filter, which is the only reason the row survives a read.
  Do not "fix" the assert without mapping `32 ↔ Switch`, or every Switch row starts being
  filtered out of `<region>-Switch.json.gz`.

## 4. The item server, seen but not served

`items.kleientertainment.com` is skins, inventory and in-game currency. A Switch client asks for
all of this the moment it reaches the main menu, and carries on when it gets a 404 — so it is
not on the critical path, and it is not served. The requests measured on 2026-09-27, verbatim:

```
POST /iap/DST/GetPricingInfo                      {"PricingHash": 0, "ClientToken": "…"}
POST /clientitems/DST/DailyGift                   {"ClientToken": "…"}
POST /gameevents/DontStarveTogether/KeyValuesGetMulti
     {"Token": "…", "Events": [{"Event": "COOKBOOK", "Revision": 0}, {"Event": "PLANTREGISTRY", …}]}
POST /clientitems/DST/GetAllItems
     {"CheckTwitchEntitlements": true, "ClientToken": "…", "InventoryStateToken": "", "EventHash": ""}
```

`ClientToken` is the session token from LoginViaSwitch, echoed back — so the tier is already
authenticated by what login mints. The rest of the surface, from the binary:
`clientitems/DST/` `Tick`, `Swap`, `ValidateItems`, `SetItemOpened`, `OpenMysteryBox`,
`RedeemCode`, `ReportMission/`, `ReportUnknownItemType`, `VerifyGiftingReceipt`; `iap/DST/`
`BarterGainItem`, `BarterLoseItem`, `BarterDuplicateItems`, `PurchaseWithCurrency`;
`gameevents/DST/GetStatus`. GetAllItems' reply is read for `Error` and `Unchanged`
*(nso 0x188324)*.

## 5. What is still unknown

- **The join path.** A Switch host reached the lobby and kept its listing alive for five hours on
  2026-09-28 (`/lobby/write` then a keepAlive every three minutes), so hosting works — with caves,
  which is the only way Switch DST hosts: the world-creation screen has no toggle for them. No client
  has ever browsed: there is not one `/lobby/read` or `.json.gz` fetch in the service's whole
  history. Until one happens we do not know whether the listing a Switch host writes carries the
  address a second console needs. `__addr` is the field for it, and on real Klei the lobby fills
  it from the source IP of the write — which OpenPak cannot do, because Traefik's TCP router
  splices the stream and `kleid` only ever sees an internal 10.89.x. `/lobby/write` now logs the
  whole listing so the next host start answers this.
- Whether `Username` has to satisfy anything (length, uniqueness); DST shows it in the browser.
- The skin/inventory tier (`items.kleientertainment.com`), which the main menu will reach for
  once login succeeds. Not served: a skinless account is a working account.
- Whether a Switch host needs anything from us for NAT traversal. The game has its own
  (`nxInternetStartup: Start NAT session success.`) and the listing carries a `nat` field.

The way to close each of these is the same one that produced this file: put the name in front of
`kleid`, read the request it logs, and disassemble the handler that consumes the reply.
