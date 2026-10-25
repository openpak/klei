# Notes: klei

> **Current Switch client (2026-10-13): Harbor**, at
> `~/REPOS/Openpak/emulators/harbor`. Harbor is an independent
> OpenPak project based on Eden. Citron, Ryujinx (desktop and Android), and
> the former OpenPak Eden checkouts have been removed. References to those
> forks below record historical evidence only: do not run their commands,
> reinstall them, or select them for current work. Use Harbor's README and
> current handoff for builds and testing; old results do not establish Harbor parity.

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
