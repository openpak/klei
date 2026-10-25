# Evidence: klei

## Status: login is built, the browser is built, neither is console-verified

DST's failure today is one request. `POST /login/LoginViaSwitch` carries the console's NSA
id_token, real Klei cannot have Nintendo vouch for a token OpenPak signed, and it answers HTTP
500 — which is the "connection to Klei failed" on screen (measured 2026-09-27, Ryujinx SSL
trace). This service answers it instead.

Nothing above that has been exercised by a real client yet. The lobby routes are built to the
contract in [`protocol.md`](protocol.md) and log every request in full, so the first
host run says what the remaining gaps are. Known gaps are listed in §4 of that file — the
nearest one is `/login/TokenPurpose`, which is how a host gets its broadcast token.
