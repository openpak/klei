# Evidence: klei

## Login failure on the real service

Against real Klei, DST's failure is one request. `POST /login/LoginViaSwitch` carries the console's NSA
id_token, real Klei cannot have Nintendo vouch for a token OpenPak signed, and it answers HTTP
500 — which is the "connection to Klei failed" on screen (measured 2026-09-27, Ryujinx SSL
trace). This service answers it instead.

The lobby routes are built to the contract in [`protocol.md`](protocol.md) and log every request
in full. Known gaps are listed in §4 of that file — the nearest one is `/login/TokenPurpose`,
which is how a host gets its broadcast token.
