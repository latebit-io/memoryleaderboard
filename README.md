# Memory Leaderboard Adapter

This repository implements the Agent Memory Leaderboard Add/Search HTTP contract over a versioned demarkus document store. It stores lossless conversation input, adds retrieval metadata, and uses a scoped navigation agent to rank evidence.

No official leaderboard score is claimed here. Run results are environment-, dataset-, and model-dependent.

## Method

`POST /add` writes one immutable markdown document per `request_id` under an injectively encoded `user_id` subtree. Raw messages remain in the body. A configured LLM distillation pass produces a title, factual summary, tags, and importance; deterministic metadata is used if distillation fails.

`POST /search` gives a nib agent only lookup, list, and fetch tools rooted in that user's subtree. The agent submits fetched evidence in relevance order. Production sets `ADAPTER_NAV=require`, so missing credentials or navigation failure cannot silently become catalog-only search.

User isolation is enforced by encoded storage paths and by navigation path validation. Add is synchronous: a successful response means the memory is searchable.

## API

All Add/Search calls require one of `X-Api-Key`, `Authorization: Bearer`, or `Authorization: Token`. Health endpoints are public.

### Add

```bash
curl -fsS http://localhost:8080/add \
  -H 'Content-Type: application/json' \
  -H "X-Api-Key: $ADAPTER_API_KEY" \
  -d '{
    "request_id":"req-001",
    "user_id":"user-001",
    "session_id":"session-001",
    "messages":[
      {"role":"user","content":"Deploys require a database backup first.","timestamp":1786615200000}
    ]
  }'
```

Required fields: non-empty `request_id`, `user_id`, `session_id`, and `messages`; every message needs non-empty `role` and `content`. Repeating identical input is idempotent. Reusing a request ID with different messages returns `409`.

### Search

```bash
curl -fsS http://localhost:8080/search \
  -H 'Content-Type: application/json' \
  -H "X-Api-Key: $ADAPTER_API_KEY" \
  -d '{"query":"deployment safeguards","user_id":"user-001","top_k":5}'
```

Search returns `{"data":[...]}`. Each result can include `id`, `content`, descending rank hint `score`, and source-derived `created_at`. `top_k` must be positive.

### Health

`GET /health` and `GET /healthz` return mode state without authentication. Production should report `"nav":true` and `"distill":true`.

## Local Development

Requirements: Go 1.26.6, local `demarkus-server` and `demarkus-token` binaries for smoke tests, and an OpenAI-compatible credential for agentic tests.

```bash
go test ./...
./scripts/smoke.sh
read -r -s ADAPTER_LLM_API_KEY && export ADAPTER_LLM_API_KEY
ADAPTER_DISTILL=require LLM_BASE_URL=https://provider.example/v1 LLM_MODEL=provider-model ./scripts/nav-smoke.sh
```

`scripts/smoke.sh` covers contract shape, auth, and user isolation with catalog search. `scripts/nav-smoke.sh` covers live agentic retrieval and skips when no provider is available.

## Hosted Deployment

The production package is a single-host Docker Compose deployment: Caddy terminates public TLS, the adapter serves private HTTP, and demarkus 0.22.7 remains reachable only on an internal Docker network.

```bash
cp .env.example .env
./scripts/bootstrap-secrets.sh
docker compose build adapter
docker compose up -d
docker compose ps
```

Set real `DOMAIN`, `ACME_EMAIL`, `LLM_BASE_URL`, and `LLM_MODEL` values in `.env` before startup. `bootstrap-secrets.sh` prompts for the provider API key without printing it; automation can use `--llm-key-file`. Full DNS, TLS, backup, retention, smoke, and rollback procedures are in [docs/operations.md](docs/operations.md).

## Configuration

| Setting | Production value | Purpose |
|---|---|---|
| `ADAPTER_NAV` | `require` | Fail instead of degrading agentic search |
| `ADAPTER_DISTILL` | `require` | Fail startup unless Add-time distillation is configured |
| `LLM_BASE_URL` | required | OpenAI-compatible provider endpoint |
| `LLM_MODEL` | required | Fixed model declared for the submitted version |
| `ADAPTER_API_KEY` | Compose secret | Public Add/Search authentication |
| `DEMARKUS_TOKEN` | Compose secret | Capability scoped to `/u/**` |
| `DEMARKUS_MAX_STREAMS` | `256` | QUIC stream capacity |
| `DEMARKUS_RATE_LIMIT` | `256` requests/s | Internal per-IP pacing |
| `DEMARKUS_RATE_BURST` | `512` | Internal burst capacity |

Commercial/Industry submissions are not subject to the Academic board's `gpt-4o-mini` checklist item. Keep the selected provider and model fixed for the declared version so reproduced behavior remains comparable.

## Evaluation

Run unit tests and both smoke scripts before an external evaluation. Local conformance, synthetic quality, and concurrent load tooling is documented in [eval/README.md](eval/README.md):

```bash
./scripts/eval-local.sh conformance
ADAPTER_NAV=off ./scripts/eval-local.sh quality
./scripts/eval-local.sh load --adds 32 --users 4 --searches 64 --concurrency 16
```

Point the official harness at `https://$DOMAIN`, use the generated adapter API key, and allow at least 130 seconds for Search. Keep the official dataset, judge, concurrency, and model settings with the result; local eval and smoke output are not official scores.

The deployment is sized for at least 16 concurrent requests, but provider quotas and host resources still need a 16-worker load check before submission. Declare limits exactly as listed in the operations guide.

## Submission Notes

Submit the HTTPS base URL only, with no endpoint suffix. Supply the API key through the leaderboard's secret channel. Do not expose demarkus UDP or publish repository secret files.

Declare the fixed provider/model in the commercial submission notes. This implementation returns ranked evidence, not a generated final answer.

## Origins And Current Work

[demarkus](https://github.com/latebit-io/demarkus) and [nib](https://github.com/latebit-io/nib) are original Latebit work. Current repository work adapts them to the leaderboard contract, adds scoped memory distillation/navigation, and packages the adapter for authenticated hosted operation. This disclosure is provenance, not an official endorsement or score claim.

## License

Use and distribution follow this repository and its dependency licenses. Confirm commercial terms for the private demarkus image and hosted model account before deployment.
