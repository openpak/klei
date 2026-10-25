# Klei

OpenPak's Klei Server.

| | |
| --- | --- |
| Network | Nintendo Switch |
| Protocol | HTTP (TLS terminated by Traefik) |
| Image | `ghcr.io/openpak/klei` |
| Licence | AGPL-3.0-only |

## Titles

| Game | Title ID | Module |
| --- | --- | --- |
| [Don't Starve Together](https://openpak.org/titles#t-143) | `010090100E334000` | `cmd/kleid` |

One process serves every Klei title. There is no per-title module directory: the title is wired into `cmd/kleid`.

## What it serves

| Host | What | Served by |
| --- | --- | --- |
| `login.kleientertainment.com` | Klei accounts: `POST /login/LoginViaSwitch` | `kleid` |
| `lobby-v2-cdn.klei.com` | Region list and pre-baked server listings | `kleid` |
| `lobby-v2-us-east-1.klei.com` | Live server browser (`/lobby/*`) | `kleid` |

## Run

```sh
NX_INTERNAL_URL=http://127.0.0.1:20070 NX_INTERNAL_KEY=… go run ./cmd/kleid
```

| Name | Default | Purpose |
| --- | --- | --- |
| `-addr` | `:22640` | HTTP listen address |
| `-health-addr` | `:22641` | Health listen address |
| `-token-secret` (`KLEI_TOKEN_SECRET`) | empty: random per boot | HMAC secret for session tokens |
| `NX_INTERNAL_URL` | `http://127.0.0.1:20070` | nx-baas internal API |
| `NX_INTERNAL_KEY` | none | Key for the nx-baas internal API |

## Docs

- [protocol.md](docs/protocol.md): hosts, endpoints, wire formats, message shapes
- [evidence.md](docs/evidence.md): captures, traces, what is confirmed and what is assumed
- [deploy.md](docs/deploy.md): ports, env vars, image, run and build detail
- [notes.md](docs/notes.md): design rationale, history, status
- [ports.md](docs/ports.md): port allocation for this network

Live compatibility status: [openpak.org/titles](https://openpak.org/titles)

## Licence

AGPL-3.0-only. See [LICENSE](LICENSE).
