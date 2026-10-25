# Deploy: klei

Plain HTTP on **22640** inside the pod network (health on **22641**); Traefik terminates the
guest's TLS for these names, the same arrangement as [`servers/t17`](../../t17) and the Among Us
front.

## Running it

```
NX_INTERNAL_URL=http://127.0.0.1:20070 NX_INTERNAL_KEY=… go run ./cmd/kleid
```

`-addr` (22640), `-health-addr` (22641) and `-token-secret` are the knobs; without a token
secret the session tokens are valid for the life of the process, which is fine while one process
serves both tiers.
