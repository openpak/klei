# Ports

Claimed from the third-party-network range (`22600–22699`, one block of ten per network), after
Among Us (22600–22609), `servers/demonware` (22610–22619), `servers/eos` (22620–22629) and
`servers/t17` (22630–22639).

| Port | Use |
| --- | --- |
| 22640 | HTTP for every Klei hostname (behind Traefik, which terminates the guest's TLS) |
| 22641 | `/health` |
| 22642–22649 | reserved for this network |

One port serves login and both lobby tiers: they are distinguished by path and hostname, not by
port, and Traefik routes all of the names to the same service.
