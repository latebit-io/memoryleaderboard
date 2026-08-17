# Local AML Evaluation

Standard-library-only tooling for local adapter checks. Results are local and non-official; they are not Agent Memory Leaderboard submissions or scores.

## Run

The wrapper reuses `scripts/lib/harness.sh`, starts an ephemeral demarkus server and adapter, and cleans both up on exit.

Run evaluator unit tests with `go test ./cmd/eval`.

```sh
scripts/eval-local.sh conformance
ADAPTER_NAV=off scripts/eval-local.sh quality
scripts/eval-local.sh load --adds 10 --users 2 --searches 20 --concurrency 4
```

Run against an existing adapter:

```sh
go run ./cmd/eval --base-url http://127.0.0.1:8080 --api-key KEY conformance
go run ./cmd/eval --base-url http://127.0.0.1:8080 --api-key KEY quality
go run ./cmd/eval --base-url http://127.0.0.1:8080 --api-key KEY load
```

`conformance` checks health, Add/Search shape and immediacy, numeric timestamps, string-array options, `top_k` caps through 100, finite descending scores, stable IDs, empty users, isolation, and configured auth. A check fails when the adapter accepts a core invalid payload instead of rejecting it with HTTP 400. Unknown fields and trailing JSON produce warnings by default; `--strict-invalid` promotes them to failures.

`quality` validates and loads the native fixture, injects `[[AML_SOURCE:id]]` markers, maps returned evidence to sources, and reports per-query latency, `recall_any@k`, `recall_all@k`, `evidence_recall@k`, `nDCG@k`, MRR, unmapped rate, and negative rate. Quality values do not control exit status; HTTP, schema, fixture, and isolation failures do.

`load` runs concurrent Add requests to completion before concurrent Search requests. It reports status distributions, latency percentiles, response-schema failures, source-marker isolation violations, and whether every expected record was visible. Tune with `--adds`, `--users`, `--searches`, `--concurrency`, and `--top-k`. Use `--adds 100 --users 1 --searches 16 --concurrency 16 --top-k 100` to exercise formal Top K fetch fan-out.

## Fixture

`fixtures/synthetic.json` follows `fixture.schema.json`. Record timestamps are numeric Unix milliseconds. Required scenarios are semantic, temporal, multi-hop, distractor, negative, and isolation. `relevant` and `negative` contain record IDs; fixture validation also prevents cross-user records from being declared relevant.
