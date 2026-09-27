# syntax=docker/dockerfile:1
# Klei Entertainment's own online stack (login + lobby-v2) for the Switch titles that use it.
# Plain HTTP inside the pod network — Traefik terminates the guest's TLS for the
# login.kleientertainment.com and lobby-v2-*.klei.com names, the same arrangement as
# servers/t17 and the Among Us front.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/kleid ./cmd/kleid

FROM gcr.io/distroless/static
COPY --from=build /out/kleid /kleid
USER nonroot:nonroot
EXPOSE 22640 22641
ENTRYPOINT ["/kleid"]
