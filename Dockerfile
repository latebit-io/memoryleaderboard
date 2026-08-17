FROM golang:1.26.6-alpine@sha256:3889b425f035be855a72fb4755265311293b6d414521f0a519d819df32222d83 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath -ldflags="-s -w" -o /out/adapter ./cmd/adapter

FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce AS runtime

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/adapter /usr/local/bin/adapter
COPY --chmod=0755 scripts/adapter-entrypoint.sh /usr/local/bin/adapter-entrypoint

USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/adapter-entrypoint"]
