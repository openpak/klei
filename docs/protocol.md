# Klei's online stack, as the Switch build speaks it

> **Current Switch client: Harbor**, at
> `~/REPOS/Openpak/emulators/harbor`. Harbor is an independent
> OpenPak project based on Eden. Citron, Ryujinx (desktop and Android), and
> the former OpenPak Eden checkouts have been removed. References to those
> forks below record historical evidence only: do not run their commands,
> reinstall them, or select them for current work. Use Harbor's README and
> current handoff for builds and testing; old results do not establish Harbor parity.

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

### Joining a server — including the host joining its own world

This is the last stretch, and all of it was measured on 2026-09-29 by watching a host go from
"Launching server" to standing in the world.

```
POST /login/GameSessionToken  {"Token":"<session token>","Server":"KU_xxxxxxxx"}
POST /login/TokenPurpose      {"Token":"<game session token>"}      <- yes, TokenPurpose again
```

**There is no `/login/ValidateToken`.** The binary carries a bare `ValidateToken` string and an
earlier draft of this file guessed a path from it; the server actually validates by posting the
player's game session token back to `/login/TokenPurpose`, which makes **three** distinct client
actions on that one path.

The sequence, and what each failure looks like on screen:

| Step | Who | Missing → what the player sees |
| --- | --- | --- |
| `GameSessionToken` | joining client | 404 → "Could not connect, unable to reach the Klei server", and the server shuts back down |
| reply's `GUID` | joining client | "[Connect] Received a GameSessionToken that is no longer valid" *(nso 0x1b8cb4 → 0x1b8d0c)*, stuck on Connecting |
| `TokenPurpose` with that token | the **server** | E_WRONG_TOKEN_PURPOSE → "Authentication failed. Please try again" |

```jsonc
// GameSessionToken
{"Error": false, "Token": "<opaque>", "GameSessionToken": "<same>",
 "GUID": "<the server's RakNet guid>", "Server": "KU_xxxxxxxx",
 "UserID": "KU_xxxxxxxx", "Username": "<display name>"}

// TokenPurpose, given a game session token
{"Error": false, "Server": "KU_xxxxxxxx", "Game": "DontStarveTogether",
 "UserID": "<the joining player>", "Username": "<name>"}
```

Two load-bearing details:

- **`GUID` is the server's RakNet guid, not the player's.** In `WAIT_TOKEN` the client compares a
  string from this reply against the server it is dialling *(nso 0x1b8c9c, the compare at
  0x1f5200, which also requires the pending connection to be in state 5)*. The request names the
  host only by `UserID`, so `kleid` takes the guid from the listing that host broadcast to the
  lobby — it is the row id, because a host brings its own guid.
- **`Server` must be the host the token was minted for.** The server compares it to its own id
  and a mismatch is "Failed to set game session token, session host mismatch" *(nso 0x171ae4)*.
  `kleid` puts the pair inside the HMAC so a token cannot be replayed against another host.

On success the server logs `New incoming connection` / `Client connected from [LAN]` and the
client reaches `[PendingConnection] -> CONNECTION_COMPLETE` and `kLoadingStatus_Done`.

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
| `/lobby/write` | `__token`, `__gameId`, `__rowId`, `masterversion` + the whole listing (34 fields), once per world start | `{"POST":{…},"result":"OK","region":"<region>"}` |
| `/lobby/update` | same, partial: 0–5 fields (`data`, `players`, `connected`, `secondaries`, `serverpaused`) | same |
| `/lobby/keepAlive` | `__token`, `__rowId` | same |
| `/lobby/delete` | `__token`, `__rowId` | same |
| `/lobby/read` | `__token`, `__gameId`, `query` (`__rowId`, `region`, …) | `{"GET":[<listing>,…]}` |
| `/lobby/getIP` | `__rowId`, `region`; a secondary shard asking for its own address | `{"__addr":"<ip>"}` *(nso 0x211790)* |

**A write is the listing; update and keepAlive are not.** A Switch host writes its whole listing
once per world start and never again; from then on it sends an update with the fields that
changed (day, players) and a keepAlive with none, alternating, so it is silent for up to five
minutes at a time (measured 2026-09-30: keepAlives at 16:44:24, 16:52:11, 17:00:10, an update
about three minutes after each). So `kleid` replaces a row on write, merges update and keepAlive
into it, keeps a listing for 15 minutes after the host was last heard from, and remembers a
hidden one for an hour so a late host comes back whole. Rebuilding a row from updates alone gives
`data`/`players`/`secondaries` and nothing else, which the browser drops ("0 of 0 shown",
2026-10-10): it is not served until the host writes again. The envelope (`__token`, `__gameId`,
`masterversion`) never reaches the listing, which `lobby-v2-cdn` serves to anyone.

A write answers `{"POST":{"__rowId":…,"__addr":…},"result":"OK","region":…}`. The host takes its
row id from `POST.__rowId` *(nso 0x1cca44, 0x1fd2f0)*, and a master shard does not read its shard
link until it holds a row and a region *(0x20ca78–0x20cac0)*: without `POST` the secondary's
`ID_SHD_S2M_SECONDARY_INFO` (0x87) is never dispatched and a world with caves hangs at launch —
on a real console exactly as on the emulators.

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

## 4a. What the client reads from the item server (static analysis, 2026-11-05; first console run corrected it)

Card openpak/prds#141. Recovered from `main.nso` (v1.29.0) with `scratch/arm64xref.py` and a disassembly; offsets are nso virtual
addresses. `KLEI_ITEMS=probe` answers these calls; off by default (the 404 is what players had).

**JSON node types** (the client's own JSON library): 1 array, 2 string, 3 number, 5 true, 6 false. Every reply below carries
`"Error": false`. **`Error` is a boolean that must be present and false**: missing, or any other type, is an error reply
("The connection to the inventory server has failed"). The first probe (v0.6.0) sent `"Error": ""` and an empty `IAPDefs`, and on a real
console (owner's Switch, test account Leia, 2026-11-05 17:36 UTC) the skins screen said "failed to download your item collection".

`POST /clientitems/DST/GetAllItems` (`GetAllItems_Complete` 0x1882cc, `ProcessInventory` 0x188b04). Request `{"ClientToken","InventoryStateToken","EventHash","CheckTwitchEntitlements"}`.
Reply: `{"Error": false, "Unchanged": false, "UserID": "<the Klei user id of the ClientToken>", "InventoryStateToken": "<opaque>", "Items": [ ... ]}`. **`UserID` matters**: the processor compares it with the signed-in user's id (nso 0x188d50) and rejects the whole list on a mismatch or when it is missing; v0.7.0-v0.7.2 omitted it and the console still said "failed to download your item collection" (19:28 run, every call 200). Not yet confirmed on a console. `Unchanged: true` (with `Error: false`) tells the
client to keep its cached list. Optional `UserID` (string) and `OfflineUserID` (string). Each item entry: `ItemID` (**number**, required),
`ItemType` (**string**, required, the game's own item name such as `wilson_formal`), and optionally `Context` (number, default 1), `Modified` (number),
`BoxMessage`, `BoxBuildOverride` (strings). An entry without a numeric ItemID and a string ItemType is logged "Malformed item entry received from
item server" and skipped. The body is cached on the console under a hash of its text. `ItemType` is the item key the Lua code asks
`TheInventory:CheckOwnership(item_key)` about; skin keys are in `scripts/skin_assets.lua` / `skin_strings.lua` of the game (`<character>_<variant>`).

`POST /clientitems/DST/DailyGift` (`DoDailyGift_Complete` 0x181574). Reply `{"Error": false, "GiftReceived": false}`; `GiftReceived: true` with an `ItemType` string
hands the player that item.

`POST /gameevents/DontStarveTogether/KeyValuesGetMulti` (`ProcessStorageData` ~0x182880). Reply `{"Events": [{"Event": "COOKBOOK", "Revision": n, "KeyValues": {...}}, ...]}`;
`Unchanged: true` per event keeps the local copy. An `Error` that is a string is logged and ends processing. An empty `Events` starts the player empty.

`POST /iap/DST/GetPricingInfo` (`ProcessPricingData` 0x17f9d4). Reply `{"Error": false, "Unchanged": false, "PricingHash": <number>, "IAPDefs": [ ... ]}`. `IAPDefs` must be
a **non-empty** array ("Missing IAPDefs" otherwise); each def needs `ItemType` (string) and either `KleiCurrency` (string) with `KleiCurrencyCost` (number), or `NxItemId`
(string); `SalePercent` and `SaleEnd` are optional numbers. Whether the skins screen needs this to succeed is not known: the server answers it only with `KLEI_ITEMS_PRICING=on`.

Test switch: `KLEI_ITEMS_TEST=wilson_formal,...` is the list of item types every account is shown as owning (the per-account list from Rewards replaces it).

**More startup calls (found in the owner's 18:35 run; replies in `KLEI_ITEMS=probe`).**

- `GET /iap/dst/GetShopEpoch` (lower case `dst`; asked by `PurchasePackScreen`, Lua only, not in the NSO). Reply `{"Time": <unix seconds>}`; the Shop warns the player ("shop epoch is wrong") when it differs from the console's clock by more than 60 s, so it is the current time.
- `POST /iap/DST/PurchaseCreateAndComplete` (body: `ClientToken`, `Environment`, `PlatformAuthToken`, `TitleId`, `UserID`; the token is the console's eShop credential and is never read). Asked at every start after the shop-service consumable list. Reply read by `OnItemServerPurchaseCompleted` (nso 0x277e30): `Error` must be false, `Message` "Purchase Completed" marks success, `Items` an optional array. OpenPak answers `{"Error": false, "Message": "Purchase Completed", "Items": []}`: nothing to settle. Either way the game then calls `OnCheckUnusedPurchasesComplete` and the Shop opens.
- The Lua text "failed to download your item collection" (`PLAYERSUMMARYSCREEN.FAILED_INVENTORY_BODY`) is shown when `TheInventory:HasDownloadedInventory()` is false. The native flag is the result of the inventory processor (nso 0x188b04). Still open on the console after the three replies above: what else makes it false.

## 4b. The in-game Shop and "Loading..." (card #141, 2026-11-05)

The owner's console run showed the Shop stuck on "Loading...". From the game's Lua (RomFS `scripts/screens/redux/multiplayermainscreen.lua`,
`scripts/screens/checkunusedpurchasespopup.lua`) and `main.nso`:

1. On the Switch the Shop button first pushes `CheckUnusedPurchasesPopup` ("Loading...") and calls `TheInventory:CheckUnusedPurchases()`. The popup closes, and the
   Shop opens, only when the native code calls back `OnCheckUnusedPurchasesComplete` (nso 0x278030). It has no timeout.
2. The native side ("[NX Store] Checking for unconsumed purchases...", 0x276a34) asks the **Nintendo eShop shop service** for the game's consumable products:
   `GET pearljam.hac.<env>.eshop.nintendo.net/civil/v1/consumables/BOLTPACKS0000001/items?country=<CC>&lang=<lang>` (also at the main menu, seen at 17:36 UTC).
   That host is nx-shop's pearljam pass-through. Parser at 0x277310: `{"inc_tax_message": "", "length": n, "total": n, "consumable_items": [ {item_id, ns_uid, name, description, price: {regular_price, formatted_value}} ]}`.
   Without `consumable_items` it logs "Error parsing JSON while receiving product list" and never completes, which is the hang. The old reply was `{}`.
   **Fix (nx-shop v0.3.1):** answer with `consumable_items: []`. OpenPak sells nothing for money, so the currency-pack list is empty and the game never offers a real-money purchase.
3. Only after that does the Shop screen (`PurchasePackScreen`) open. It reads `TheItems:GetIAPDefs()` (from `GetPricingInfo`) and shows an empty-list text when there are none.
   The shop's own purchase calls are `iap/DST/PurchaseCreateAndComplete` (an eShop purchase to be consumed), `iap/DST/PurchaseWithCurrency` (virtual currency), `iap/DST/BarterGainItem`,
   `BarterLoseItem`, `BarterDuplicateItems`, and `clientitems/DST/OpenMysteryBox`, `Swap`, `ValidateItems`, `RedeemCode`, `Tick`, `ReportMission/<name>`,
   `SetItemOpened`, `ReportUnknownItemType`, `VerifyGiftingReceipt`, `gameevents/DST/GetStatus`. None is answered: with no products and no IAP defs nothing can be bought.

## 4c. Owned cosmetics from Rewards (card #141)

`KLEI_ITEMS=rewards` replaces the test list: `GetAllItems` lists the DST skins the player owns in Rewards. kleid names the player by network identity
(`namespace=switch`, `subject` = the BAAS user id the Klei id was minted for, from the persisted Klei-id map) and asks Rewards'
`GET /internal/v1/adapter/inventory?...&game=dst` with the events key it already holds (`REWARDS_EVENTS_URL`, `REWARDS_EVENTS_KEY`). Answers are cached 60 s per
player and dropped at once on the account core's `entitlement_changed` (subject `dst`) through `SubscribeAccountEvents`; a Rewards outage serves the last answer for
a day. An item's `ItemType` is its game key (`wilson_formal`); only keys Rewards marks deliverable are returned. A player nobody linked to an OpenPak account owns nothing.

**Equip is local.** DST has no equip call to the item server: the calls it makes are `GetAllItems`, `DailyGift`, `KeyValuesGetMulti/Set(Multi)`, `Swap`, `ValidateItems`,
`SetItemOpened`, `OpenMysteryBox`, `RedeemCode`, `Tick`, `ReportMission`, `ReportUnknownItemType`, `VerifyGiftingReceipt` and the `iap/DST/*` shop calls. The chosen skin is kept
in the player's own profile on the console, so nothing is reported to Rewards (no `PUT /adapter/equipped`).

`tools/catalog/gen_catalog.py` builds the Rewards import list from the game's scripts: 431 character skins (`type = "base"` in `prefabs/skinprefabs.lua`), named from
`skin_strings.lua` (`wilson_formal` is "Guest of Honor"), priced by the game's rarity (Heirloom* legendary 400, Elegant epic 200, Complimentary common 30; Event and Loyal are
earned, not sold), `deliverable` when the skin's build `anim/dynamic/<name>.zip` is in the installed RomFS (430 of 431).

**The in-game Shop sells for points (`KLEI_ITEMS=rewards` with `KLEI_ITEMS_PRICING=on`).** The Shop screen sells *packs* (`pack_*` in `misc_items.lua`, 493 of type "purchase"); buying one gives its `output_items`
(e.g. `pack_wilson_formal` gives `wilson_formalp`, `body_wilson_formalp`, ...). Rewards' catalogue therefore sells the packs (slot `pack`, name from `skin_strings.lua`, 487 imported, price from the best
rarity inside: 80 or 200 points), so both shops are one catalogue and one ledger.
- `GetPricingInfo`: one `IAPDefs` entry per pack on sale in Rewards (`ItemType` the pack key, `KleiCurrency` "BOLTS", `KleiCurrencyCost` the points price), plus the `openpak_none` placeholder. Whether the Shop screen shows
  the defs is decided by its filter (`in_store`, a known `MISC_ITEMS` type, virtual currency); not yet seen on a console.
- `GetAllItems` adds `CurrencyMap: {"BOLTS": <points>}` (nso 0x18bbcc): the game's "bolts" counter is the player's Rewards points.
- `PurchaseWithCurrency` (body `ItemType`, `TransID`, `ExpectedPrice`, `ClientToken`; nso 0x266430, reply read at 0x173090): kleid calls Rewards `POST /internal/v1/adapter/redeem` and answers `{"Error": false, "NewCurrency": <points left>}`;
  not enough points is `{"Error": true, "ErrorCode": "E_BALANCE_MISMATCH"}`, a changed price or an unsellable pack `E_PRICE_MISMATCH`. A retried `TransID` is charged once. The purchase drops the player's cached inventory, so
  `GetAllItems` lists the pack's items at once; a pack is not kept as an item.
- Real money stays out: `PurchaseCreateAndComplete` and the eShop consumable list answer "nothing", `Barter*` and the rest are unanswered.

## 4d. Which chests are sold, and the game's purchase rules (card #150, 2026-11-06)

What Klei sold for money is the Shop's chests: the 493 `type = "purchase"` entries of `misc_items.lua`. Six give in-game currency (`output_klei_currency`,
no OpenPak equivalent: points are the currency). Of the 487 that hand out items, **481 have a Shop picture** (`display_atlas`) and are sold; the other six
(`pack_hl_gift`, `pack_oni_gift`, `pack_rog_gift`, `pack_sw_gift`, `pack_hamlet_wormwood`, `pack_boomerang_psn`, rarity `Loyal`) have none: they are gifts for owning
another product and stay earned (origin `earned`, no price). Single skins are never sold alone: a character skin row (`wilson_formal`, `Heirloom*`, the woven `...p`
variants) is either a chest's content or earned (Heirloom, Event, Loyal, Complimentary), so skin rows carry no price and the chest is what is listed.

The one rule the game applies before a sale is `IsPackRestrictedDueToOwnership` (`skinsutils.lua`, used by `purchasepackscreen.lua`): a chest holding a base skin
for a restricted character (`is_restricted`: Wanda, Wormwood, Wortox, Wurt) needs that character owned (`<name>_none`), unless the chest includes the character itself.
The game only warns and lets a player continue; OpenPak refuses. 108 chests carry it. The generator writes it as `requires` and a chest's contents as `grants`;
Rewards 0.27.0 enforces `requires` on every redeem (website, console shops, this Shop), and a chest that gives the character (`pack_character_*`, `pack_*_deluxe`,
`pack_starter_*`) satisfies it. Re-buying a chest the player owns is refused (a points ledger holds one copy); the game would warn and allow it.

Not sold, as in the game: drops (`Tick`), weaving with spools (`Swap`, `Barter*`), mystery boxes, event, loyalty and achievement items, and the starting defaults.
Those calls are not answered (404) and the defaults need nothing from a server; items earned through OpenPak missions are granted in Rewards (origin `event`/`earned`,
draft, never activatable for event items) and listed in `GetAllItems` like any owned item.

## 5. What is still unknown

- **The join path.** A Switch host reached the lobby and kept its listing alive for five hours on
  2026-09-28 (`/lobby/write` then updates and keepAlives, see §3), so hosting works — with caves,
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

## From the previous README

The service a Klei Switch title reaches when a console is redirected to OpenPak. Today that is
**Don't Starve Together** (`010090100E334000`). One process serves both tiers, dispatching on
path and hostname:

| Host | What it answers |
| --- | --- |
| `login.kleientertainment.com` | `POST /login/LoginViaSwitch` — the console's own BAAS token in, a Klei user out |
| `lobby-v2-cdn.klei.com` | the region list and the pre-baked `<region>-Switch.json.gz` listings |
| `lobby-v2-us-east-1.klei.com` | `/lobby/write\|update\|keepAlive\|delete\|read` — the live server browser |

`galette.klei.com` and `cdn-galette.klei.com` are deliberately **not** redirected: they serve the
game's own content and they work against real Klei.

## Design decisions

- **Token verification.** `LoginViaSwitch` carries a BAAS `id_token` that OpenPak signed. Rather
  than checking the signature itself, `kleid` asks nx-baas's `/internal/switch/identity`, which
  also returns the player's nickname (their Klei `Username`), whether the account is banned, and
  "this player is online now" (what the titles grid counts playtime from).
- **Real Klei rejects the console.** Against real Klei the failure is one request:
  `POST /login/LoginViaSwitch` carries the NSA id_token, real Klei cannot have Nintendo vouch for
  a token OpenPak signed, and answers HTTP 500, shown as "connection to Klei failed" (measured
  2026-09-27 with an SSL trace).
- **The listing is the host's.** A DST server listing has forty-odd fields from the host's build.
  `kleid` stores the object the host wrote and returns it unchanged, adding only `__rowId`.
  Two builds then agree with each other rather than with us.
