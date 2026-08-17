# Hosted Operations

This runbook operates the single-host commercial Agent Memory Leaderboard submission package. Commands run from the repository root unless stated otherwise.

## DNS And Host

Provision a Linux host with Go 1.26.6, Docker Engine and Docker Compose, full-disk encryption for Docker volumes and backups, persistent local storage, outbound HTTPS, and inbound TCP 80/443. Do not open UDP 6309: demarkus is private to the Compose `storage` network.

Create an `A` record for the submission hostname pointing to the host's public IPv4 address. Add `AAAA` only when inbound IPv6 is tested. Wait for public DNS to resolve to this host before starting Caddy.

Copy `.env.example` to `.env`, then set `DOMAIN` to the DNS hostname without scheme/path and `ACME_EMAIL` to the ACME account contact. Keep the MiniMax endpoint and model unchanged while validating the submission candidate. Never commit `.env`.

## Initial Deploy

Authenticate Docker to `ghcr.io` with an account authorized for the private demarkus image. The deployment pins demarkus 0.22.7 to an exact manifest digest.

```bash
./scripts/bootstrap-secrets.sh
docker compose pull demarkus caddy volume-init
docker compose build --pull adapter
docker compose up -d
docker compose ps
```

For noninteractive secret bootstrap, place the real LLM provider key in a protected file and pass `--llm-key-file`. The script generates independent adapter and demarkus tokens, stores only the demarkus token hash in `demarkus-tokens.toml`, and refuses overwrite. The secret directory is `0700`; files are `0404` because non-Swarm Compose bind-mounts source files without applying declared ownership. Parent-directory traversal prevents other host users from reading them, while container UID 65532 can read each direct mount.

## Model Baseline

The validation candidate uses MiniMax's OpenAI-compatible API:

```dotenv
LLM_BASE_URL=https://api.minimax.io/v1
LLM_MODEL=MiniMax-M3
```

MiniMax-M3 is not frozen for submission until live Add distillation, agentic Search, quality, and 16-worker load checks pass with these exact values. Record the final model and endpoint with the submitted version.

Expected health response:

```bash
curl -fsS "https://$DOMAIN/healthz"
```

It must include `"status":"ok"`, `"nav":true`, and `"distill":true`.

## TLS

Caddy obtains and renews public certificates automatically through ACME. Host ports 80 and 443 map to Caddy's nonroot ports 8080 and 8443. Keep both public ports reachable for challenge and redirect handling. Caddy state lives in `caddy_data`; do not routinely delete it or certificate issuance rate limits may apply.

Check certificate and redirect behavior:

```bash
curl -fsSI "http://$DOMAIN/healthz"
curl -fsSI "https://$DOMAIN/healthz"
docker compose logs --since=10m caddy
```

## Authentication

`/add` and `/search` require the value in `secrets/adapter-api-key` as `X-Api-Key`, `Authorization: Bearer`, or `Authorization: Token`. `/health` and `/healthz` are intentionally public and contain no customer data.

The adapter's demarkus capability is restricted to read/publish under `/u/**`. Raw demarkus credentials and `demarkus-tokens.toml` never enter the image or Git. Rotate credentials only during downtime: replace all related files atomically, then recreate affected services.

## Capacity And Limits

Production settings support at least 16 concurrent adapter requests:

| Limit | Declaration |
|---|---|
| Public HTTP request rate | No edge rate limit; upstream/model capacity is the practical bound |
| Public request body | 8 MB at Caddy |
| Adapter read timeout | 30 seconds |
| Adapter Search/write timeout | 120 seconds |
| Caddy upstream response-header timeout | 125 seconds |
| Recommended client Search timeout | At least 130 seconds |
| Navigation budget | 90 seconds |
| Add distillation budget | 30 seconds; deterministic fallback on failure |
| demarkus request timeout | 30 seconds per Mark request |
| demarkus QUIC streams | 256 per connection |
| demarkus internal rate | 256 requests/s per adapter IP, burst 512 |

Run a 16-worker load check with representative Add/Search payloads before submission:

```bash
ADAPTER_API_KEY=$(tr -d '\n' < secrets/adapter-api-key)
go run ./cmd/eval --base-url "https://$DOMAIN" --api-key "$ADAPTER_API_KEY" \
  --timeout 130 load --adds 100 --users 1 --searches 16 --concurrency 16 --top-k 100
```

Watch provider rate-limit responses, CPU, memory, disk latency, and p95 latency. Increase host/provider capacity before changing these declared limits.

## Backups

Backups are filesystem-consistent and cause brief automatic downtime.

```bash
./scripts/backup.sh
```

If the deployment was started with `docker compose -p NAME`, export `COMPOSE_PROJECT_NAME=NAME` before backup or restore commands. The script serializes backups with `.backup.lock`, records which data services are active, stops both before reading the volume, and reliably restarts previously active services on success, failure, or interruption. It writes mode-`0600` archive and checksum temporary files under ignored `backups/`, then publishes both only after successful completion. Copy both files to encrypted off-host storage with access controls matching production data.

### Restore

Verify the checksum and restore only into an empty demarkus volume. Set `BACKUP` to the selected archive's absolute path.

```bash
set -euo pipefail
: "${BACKUP:?set BACKUP to an absolute archive path}"
COMPOSE_FILE=compose.yaml
LOCK_DIR=$(pwd)/.backup.lock
if ! mkdir "$LOCK_DIR" 2>/dev/null; then
  echo "another backup or restore is active" >&2
  exit 1
fi
trap 'rmdir "$LOCK_DIR" 2>/dev/null || true' EXIT
trap 'exit 130' HUP INT TERM
docker compose -f "$COMPOSE_FILE" create volume-init
INIT_CONTAINER=$(docker compose -f "$COMPOSE_FILE" ps -a -q volume-init)
test -n "$INIT_CONTAINER"
VOLUME=$(docker inspect --format \
  '{{range .Mounts}}{{if eq .Destination "/demarkus-data"}}{{.Name}}{{end}}{{end}}' \
  "$INIT_CONTAINER")
test -n "$VOLUME"
(cd "$(dirname "$BACKUP")" && sha256sum -c "$(basename "$BACKUP").sha256")
docker volume inspect "$VOLUME" >/dev/null
docker compose -f "$COMPOSE_FILE" down
docker volume rm "$VOLUME"
if docker volume inspect "$VOLUME" >/dev/null 2>&1; then
  echo "volume still exists after removal: $VOLUME" >&2
  exit 1
fi
if ! VOLUMES=$(docker volume ls --quiet --filter "name=^${VOLUME}$"); then
  echo "failed to verify volume removal" >&2
  exit 1
fi
if printf '%s\n' "$VOLUMES" | grep -Fx "$VOLUME" >/dev/null; then
  echo "volume still listed after removal: $VOLUME" >&2
  exit 1
fi
BACKUP_DIR=$(dirname "$BACKUP")
BACKUP_NAME=$(basename "$BACKUP")
docker compose -f "$COMPOSE_FILE" run --rm --no-deps --user 0:0 \
  -e "BACKUP_NAME=$BACKUP_NAME" -v "$BACKUP_DIR:/restore:ro" volume-init \
  sh -ec 'tar -C /demarkus-data -xzf "/restore/$BACKUP_NAME"'
docker compose -f "$COMPOSE_FILE" up -d
```

Check `/healthz`, authenticate an Add/Search smoke, and inspect demarkus startup logs for catalog/hash-index errors.

## 30-Day Deletion

Run the retention purge daily. It defaults to dry-run:

```bash
./scripts/purge-expired.sh
```

After reviewing candidates and taking a backup:

```bash
docker compose stop adapter demarkus
./scripts/purge-expired.sh --execute
docker compose start demarkus adapter
```

`--execute` is mandatory for deletion and refuses while either data service runs. `--days N` changes the default 30-day threshold.

Backups contain the same evaluation data and share the 30-day limit. Review and delete expired local copies daily:

```bash
./scripts/purge-backups.sh
./scripts/purge-backups.sh --execute
```

Configure the encrypted off-host backup destination with a lifecycle of 30 days or less; local scripts cannot enforce remote retention.

The threshold uses the current version file's mtime, which records ingestion/write time, not a customer-supplied message timestamp. The script accepts only adapter-generated base32 paths matching `/u/<user>/sessions/<session>/<request>.md`; it removes that current symlink and its matching version directory. It never traverses or deletes paths outside `/u/`.

This procedure depends on demarkus server 0.22.7's file layout: current document symlink `<dir>/<doc>.md` targeting `versions/<doc>.md/vN`, with immutable versions below that directory. Do not run purge after a demarkus version change until the new layout has been reviewed and this script updated. Purge is irreversible independent of backup retention.

## Logs And Privacy

Caddy access logs and service logs go to container stdout in JSON/text form. Configure host log rotation and ship only to an access-controlled destination. Caddy removes `X-Api-Key` from access logs and automatically redacts Authorization; it does not log request bodies. Do not enable body/debug tracing in production. Treat URLs, user IDs, document paths, errors, and IP addresses as potentially sensitive metadata.

Raw messages and distilled summaries are stored in ordinary Docker volumes, so the host must provide full-disk encryption; Compose does not add encryption. They are sent to the configured LLM API for distillation/navigation. Document model-provider processing, retention, region, and subprocessors in customer disclosures. Restrict host, Docker socket, backup, `.env`, and `secrets/` access. Never include secrets or customer payloads in support bundles.

Inspect recent logs without dumping historical customer activity:

```bash
docker compose logs --since=10m adapter demarkus caddy
```

## Production Smoke

Read the key into the shell without printing it, use a dedicated smoke user, and remove its data through the retention process.

```bash
ADAPTER_API_KEY=$(tr -d '\n' < secrets/adapter-api-key)
curl -fsS "https://$DOMAIN/add" \
  -H 'Content-Type: application/json' -H "X-Api-Key: $ADAPTER_API_KEY" \
  -d '{"request_id":"prod-smoke-1","user_id":"prod-smoke","session_id":"smoke","messages":[{"role":"user","content":"Production smoke marker."}]}'
curl -fsS "https://$DOMAIN/search" \
  -H 'Content-Type: application/json' -H "X-Api-Key: $ADAPTER_API_KEY" \
  -d '{"query":"production smoke marker","user_id":"prod-smoke","top_k":3}'
```

Confirm `200`, correct response shape, relevant evidence, and no records from another user. Also verify a request without the key returns `401`.

## Upgrade And Rollback

Before changes, take a backup and retain the current immutable adapter image. Never float the demarkus tag or digest.

```bash
docker image tag memoryleaderboard-adapter:local memoryleaderboard-adapter:rollback
docker compose build --pull adapter
docker compose up -d
```

Rollback adapter/Caddy without changing data:

```bash
ADAPTER_IMAGE=memoryleaderboard-adapter:rollback docker compose up -d --no-build adapter caddy
```

If a demarkus upgrade started against the volume, restore the pre-upgrade backup before returning to 0.22.7. Validate health, auth, model mode, Add idempotency, Search isolation, and a 16-worker load check after every deploy or rollback.
