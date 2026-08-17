#!/usr/bin/env python3
"""Local, non-official Agent Memory Leaderboard evaluation client."""

from __future__ import annotations

import argparse
import concurrent.futures
import json
import math
import os
import re
import statistics
import sys
import time
import urllib.error
import urllib.request
import uuid
from collections import Counter
from dataclasses import dataclass
from pathlib import Path
from typing import Any


LABEL = "LOCAL / NON-OFFICIAL AML EVALUATION"
SOURCE_RE = re.compile(r"\[\[AML_SOURCE:([A-Za-z0-9._:-]+)\]\]")
SCENARIOS = {"semantic", "temporal", "multi-hop", "distractor", "negative", "isolation"}


@dataclass
class Response:
    status: int
    body: bytes
    elapsed_ms: float
    error: str = ""

    def json(self) -> Any:
        return json.loads(self.body.decode("utf-8"))


class Client:
    def __init__(self, base_url: str, api_key: str, timeout: float) -> None:
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.timeout = timeout

    def request(
        self,
        path: str,
        payload: Any | None = None,
        *,
        raw: bytes | None = None,
        auth: bool = True,
        api_key: str | None = None,
    ) -> Response:
        data = raw if raw is not None else (json.dumps(payload).encode("utf-8") if payload is not None else None)
        headers = {"Content-Type": "application/json"} if data is not None else {}
        key = self.api_key if api_key is None else api_key
        if auth and key:
            headers["X-Api-Key"] = key
        request = urllib.request.Request(self.base_url + path, data=data, headers=headers)
        started = time.perf_counter()
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                body = response.read()
                return Response(response.status, body, (time.perf_counter() - started) * 1000)
        except urllib.error.HTTPError as exc:
            return Response(exc.code, exc.read(), (time.perf_counter() - started) * 1000, str(exc))
        except (urllib.error.URLError, TimeoutError, OSError) as exc:
            return Response(0, b"", (time.perf_counter() - started) * 1000, str(exc))


class Checks:
    def __init__(self) -> None:
        self.passed = 0
        self.failed = 0
        self.warned = 0

    def check(self, name: str, condition: bool, detail: str = "") -> None:
        if condition:
            self.passed += 1
            print(f"PASS {name}")
        else:
            self.failed += 1
            print(f"FAIL {name}{': ' + detail if detail else ''}")

    def warn(self, name: str, detail: str) -> None:
        self.warned += 1
        print(f"WARN {name}: {detail}")

    def strict(self, name: str, condition: bool, strict: bool, detail: str) -> None:
        if condition:
            self.check(name, True)
        elif strict:
            self.check(name, False, detail)
        else:
            self.warn(name, detail + "; use --strict-invalid to fail")


def decode(response: Response) -> tuple[Any | None, str]:
    try:
        return response.json(), ""
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        return None, f"invalid JSON: {exc}; body={response.body[:200]!r}"


def result_errors(body: Any, top_k: int) -> list[str]:
    if not isinstance(body, dict) or not isinstance(body.get("data"), list):
        return ["response must be an object with data array"]
    data = body["data"]
    errors = []
    if set(body) != {"data"}:
        errors.append(f"response fields must be exactly ['data'], got {sorted(body)}")
    if len(data) > top_k:
        errors.append(f"returned {len(data)} records for top_k={top_k}")
    scores = []
    ids = []
    for index, record in enumerate(data):
        if not isinstance(record, dict):
            errors.append(f"data[{index}] is not an object")
            continue
        extra = set(record) - {"id", "content", "score", "created_at"}
        if extra:
            errors.append(f"data[{index}] has unknown fields: {sorted(extra)}")
        if not isinstance(record.get("id"), str) or not record.get("id"):
            errors.append(f"data[{index}].id must be a non-empty string")
        else:
            ids.append(record["id"])
        if not isinstance(record.get("content"), str):
            errors.append(f"data[{index}].content must be a string")
        score = record.get("score")
        if isinstance(score, bool) or not isinstance(score, (int, float)) or not math.isfinite(score):
            errors.append(f"data[{index}].score must be finite numeric")
        else:
            scores.append(float(score))
        if "created_at" in record and not isinstance(record["created_at"], str):
            errors.append(f"data[{index}].created_at must be a string when present")
    if len(scores) == len(data) and any(a < b for a, b in zip(scores, scores[1:])):
        errors.append("scores are not descending (non-increasing)")
    if len(ids) != len(set(ids)):
        errors.append("result IDs must be unique")
    return errors


def add_payload(request_id: str, user_id: str, session_id: str, content: str, timestamp: int) -> dict[str, Any]:
    return {
        "request_id": request_id,
        "user_id": user_id,
        "session_id": session_id,
        "messages": [{"role": "user", "content": content, "timestamp": timestamp}],
    }


def conformance(args: argparse.Namespace, client: Client) -> int:
    checks = Checks()
    run = uuid.uuid4().hex[:12]
    user_a = f"local-conf-a-{run}"
    user_b = f"local-conf-b-{run}"
    now = 1786838400000

    health = client.request("/health")
    health_body, health_error = decode(health)
    checks.check(
        "/health returns healthy JSON",
        health.status == 200 and isinstance(health_body, dict) and health_body.get("status") == "ok",
        health_error or f"status={health.status} body={health.body[:200]!r}",
    )

    marker_a = f"confalpha{run}"
    first = add_payload(
        f"conf-add-1-{run}", user_a, "session-1", f"{marker_a} parser build requires make lint", now
    )
    added = client.request("/add", first)
    added_body, added_error = decode(added)
    expected_echo = {
        "success": True,
        "request_id": first["request_id"],
        "user_id": user_a,
        "session_id": "session-1",
    }
    checks.check(
        "numeric timestamp accepted and Add echo exact",
        added.status == 200 and added_body == expected_echo,
        added_error or f"status={added.status} body={added_body!r}",
    )

    marker_b = f"confbravo{run}"
    second = add_payload(
        f"conf-add-2-{run}", user_b, "session-2", f"{marker_b} private deployment uses caddy", now + 1
    )
    second_response = client.request("/add", second)
    checks.check("cross-user fixture Add succeeds", second_response.status == 200, f"status={second_response.status}")

    third = add_payload(
        f"conf-add-3-{run}", user_a, "session-1", f"{marker_a} secondary parser evidence", now + 2
    )
    third_response = client.request("/add", third)
    checks.check("second same-user Add succeeds", third_response.status == 200, f"status={third_response.status}")

    search_payload = {"query": marker_a, "options": ["A. parser", "B. deploy"], "user_id": user_a, "top_k": 100}
    searched = client.request("/search", search_payload)
    search_body, search_error = decode(searched)
    schema_errors = result_errors(search_body, 100) if searched.status == 200 and search_body is not None else []
    contents = "\n".join(r.get("content", "") for r in search_body.get("data", [])) if isinstance(search_body, dict) else ""
    checks.check(
        "immediate Search accepts string options and top_k=100",
        searched.status == 200 and marker_a in contents,
        search_error or f"status={searched.status} body={search_body!r}",
    )
    checks.check(
        "result schema, finite descending scores, and top_k=100 cap",
        searched.status == 200 and not schema_errors,
        "; ".join(schema_errors) or f"status={searched.status}",
    )

    capped = client.request("/search", {"query": marker_a, "user_id": user_a, "top_k": 1})
    capped_body, capped_error = decode(capped)
    capped_errors = result_errors(capped_body, 1) if capped.status == 200 and capped_body is not None else []
    checks.check(
        "top_k=1 cap",
        capped.status == 200 and not capped_errors,
        capped_error or "; ".join(capped_errors) or f"status={capped.status}",
    )

    repeated = client.request("/search", search_payload)
    repeated_body, repeated_error = decode(repeated)
    first_ids = [r.get("id") for r in search_body.get("data", [])] if isinstance(search_body, dict) else None
    repeated_ids = [r.get("id") for r in repeated_body.get("data", [])] if isinstance(repeated_body, dict) else None
    checks.check(
        "stable result IDs and order",
        searched.status == 200 and repeated.status == 200 and first_ids == repeated_ids,
        repeated_error or f"first={first_ids!r} repeated={repeated_ids!r}",
    )

    empty = client.request("/search", {"query": "nothing", "user_id": f"empty-{run}", "top_k": 100})
    empty_body, empty_error = decode(empty)
    checks.check(
        "user with no memories returns empty data array",
        empty.status == 200 and empty_body == {"data": []},
        empty_error or f"status={empty.status} body={empty_body!r}",
    )

    isolated = client.request("/search", {"query": marker_b, "user_id": user_a, "top_k": 100})
    isolated_body, isolated_error = decode(isolated)
    isolated_contents = (
        "\n".join(r.get("content", "") for r in isolated_body.get("data", []))
        if isinstance(isolated_body, dict)
        else marker_b
    )
    checks.check(
        "cross-user isolation",
        isolated.status == 200 and marker_b not in isolated_contents,
        isolated_error or f"status={isolated.status} leaked={marker_b in isolated_contents}",
    )

    if args.api_key:
        auth_payload = {"query": "x", "user_id": user_a, "top_k": 1}
        missing = client.request("/search", auth_payload, auth=False)
        wrong = client.request("/search", auth_payload, api_key=args.api_key + "-wrong")
        checks.check(
            "configured auth rejects missing and wrong credentials",
            missing.status == 401 and wrong.status == 401,
            f"missing={missing.status} wrong={wrong.status}",
        )
    else:
        checks.warn("configured auth rejection", "no --api-key supplied; check not applicable")

    invalid_cases = [
        ("malformed JSON", "/add", None, b"{"),
        (
            "string timestamp",
            "/add",
            add_payload(f"bad-time-{run}", user_a, "bad", "bad", now) | {
                "messages": [{"role": "user", "content": "bad", "timestamp": "2026-08-16T00:00:00Z"}]
            },
            None,
        ),
        ("empty user_id", "/search", {"query": "x", "user_id": "", "top_k": 1}, None),
        ("object options", "/search", {"query": "x", "options": {"A": "x"}, "user_id": user_a, "top_k": 1}, None),
        ("missing top_k", "/search", {"query": "x", "user_id": user_a}, None),
        ("non-positive top_k", "/search", {"query": "x", "user_id": user_a, "top_k": 0}, None),
    ]
    for name, path, payload, raw in invalid_cases:
        response = client.request(path, payload, raw=raw)
        checks.check(f"invalid payload rejected: {name}", response.status == 400, f"status={response.status}")

    unknown_add = add_payload(f"unknown-{run}", user_a, "strict", "strict unknown field probe", now + 3)
    unknown_add["unexpected"] = True
    strict_cases = [
        ("unknown Add field", client.request("/add", unknown_add)),
        (
            "unknown Search field",
            client.request("/search", {"query": marker_a, "user_id": user_a, "top_k": 1, "unexpected": True}),
        ),
        (
            "trailing JSON value",
            client.request(
                "/search",
                raw=json.dumps({"query": marker_a, "user_id": user_a, "top_k": 1}).encode("utf-8") + b" {}",
            ),
        ),
    ]
    for name, response in strict_cases:
        checks.strict(
            f"strict invalid payload rejected: {name}",
            response.status == 400,
            args.strict_invalid,
            f"permissive parser returned status={response.status}",
        )

    print(f"SUMMARY mode=conformance pass={checks.passed} fail={checks.failed} warn={checks.warned}")
    return 1 if checks.failed else 0


def load_fixture(path: str) -> dict[str, Any]:
    try:
        fixture = json.loads(Path(path).read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError(f"cannot read fixture {path}: {exc}") from exc
    errors = validate_fixture(fixture)
    if errors:
        raise ValueError("invalid fixture:\n  " + "\n  ".join(errors))
    return fixture


def validate_fixture(fixture: Any) -> list[str]:
    if not isinstance(fixture, dict):
        return ["root must be an object"]
    errors = []
    if type(fixture.get("version")) is not int or fixture.get("version") != 1:
        errors.append("version must be 1")
    extra_root = set(fixture) - {"version", "name", "records", "queries"}
    if extra_root:
        errors.append(f"root has unknown fields: {sorted(extra_root)}")
    if not isinstance(fixture.get("name"), str) or not fixture.get("name"):
        errors.append("name must be a non-empty string")
    records = fixture.get("records")
    queries = fixture.get("queries")
    if not isinstance(records, list) or not records:
        errors.append("records must be a non-empty array")
        records = []
    if not isinstance(queries, list) or not queries:
        errors.append("queries must be a non-empty array")
        queries = []
    record_ids = set()
    record_users = {}
    for index, record in enumerate(records):
        prefix = f"records[{index}]"
        if not isinstance(record, dict):
            errors.append(f"{prefix} must be an object")
            continue
        extra = set(record) - {"id", "user_id", "session_id", "timestamp", "content"}
        if extra:
            errors.append(f"{prefix} has unknown fields: {sorted(extra)}")
        for key in ("id", "user_id", "session_id", "content"):
            if not isinstance(record.get(key), str) or not record.get(key):
                errors.append(f"{prefix}.{key} must be a non-empty string")
        record_id = record.get("id")
        if isinstance(record_id, str):
            if not re.fullmatch(r"[A-Za-z0-9._:-]+", record_id):
                errors.append(f"{prefix}.id contains unsupported marker characters")
            if record_id in record_ids:
                errors.append(f"duplicate record id {record_id!r}")
            record_ids.add(record_id)
            record_users[record_id] = record.get("user_id")
        timestamp = record.get("timestamp")
        if isinstance(timestamp, bool) or not isinstance(timestamp, int):
            errors.append(f"{prefix}.timestamp must be numeric Unix milliseconds")
    query_ids = set()
    found_scenarios = set()
    for index, query in enumerate(queries):
        prefix = f"queries[{index}]"
        if not isinstance(query, dict):
            errors.append(f"{prefix} must be an object")
            continue
        extra = set(query) - {"id", "user_id", "scenario", "query", "relevant", "negative", "top_k"}
        if extra:
            errors.append(f"{prefix} has unknown fields: {sorted(extra)}")
        for key in ("id", "user_id", "query", "scenario"):
            if not isinstance(query.get(key), str) or not query.get(key):
                errors.append(f"{prefix}.{key} must be a non-empty string")
        query_id = query.get("id")
        if isinstance(query_id, str):
            if query_id in query_ids:
                errors.append(f"duplicate query id {query_id!r}")
            query_ids.add(query_id)
        scenario = query.get("scenario")
        if scenario not in SCENARIOS:
            errors.append(f"{prefix}.scenario must be one of {sorted(SCENARIOS)}")
        else:
            found_scenarios.add(scenario)
        for key in ("relevant", "negative"):
            if key not in query:
                errors.append(f"{prefix}.{key} is required")
                continue
            values = query[key]
            if not isinstance(values, list) or any(not isinstance(value, str) for value in values):
                errors.append(f"{prefix}.{key} must be a string array")
                continue
            if len(values) != len(set(values)):
                errors.append(f"{prefix}.{key} must not contain duplicates")
            for value in values:
                if value not in record_ids:
                    errors.append(f"{prefix}.{key} references unknown record {value!r}")
        overlap = set(query.get("relevant", [])) & set(query.get("negative", []))
        if overlap:
            errors.append(f"{prefix} has relevant/negative overlap: {sorted(overlap)}")
        for relevant in query.get("relevant", []):
            if record_users.get(relevant) != query.get("user_id"):
                errors.append(f"{prefix} marks another user's record {relevant!r} relevant")
        top_k = query.get("top_k")
        if top_k is not None and (isinstance(top_k, bool) or not isinstance(top_k, int) or not 1 <= top_k <= 100):
            errors.append(f"{prefix}.top_k must be an integer between 1 and 100")
    missing = SCENARIOS - found_scenarios
    if missing:
        errors.append(f"queries missing scenarios: {sorted(missing)}")
    return errors


def marked_content(record: dict[str, Any]) -> str:
    return f"{record['content']}\n\n[[AML_SOURCE:{record['id']}]]"


def qualify_user(run: str, user_id: str) -> str:
    return f"local-quality-{run}-{user_id}"


def dcg(relevances: list[int]) -> float:
    return sum(rel / math.log2(rank + 2) for rank, rel in enumerate(relevances))


def quality(args: argparse.Namespace, client: Client) -> int:
    try:
        fixture = load_fixture(args.fixture)
    except ValueError as exc:
        print(f"ERROR {exc}", file=sys.stderr)
        return 2
    run = uuid.uuid4().hex[:12]
    records = {record["id"]: record for record in fixture["records"]}
    operational_failures = 0
    print(f"FIXTURE name={fixture['name']} records={len(records)} queries={len(fixture['queries'])}")
    for record in fixture["records"]:
        payload = add_payload(
            f"quality-{run}-{record['id']}",
            qualify_user(run, record["user_id"]),
            record["session_id"],
            marked_content(record),
            record["timestamp"],
        )
        response = client.request("/add", payload)
        if response.status != 200:
            operational_failures += 1
            print(f"FAIL Add source={record['id']} status={response.status} body={response.body[:200]!r}")
    if operational_failures:
        print(f"SUMMARY mode=quality operational_failures={operational_failures}; scoring skipped")
        return 1

    positive_queries = 0
    recall_any_sum = 0.0
    recall_all_sum = 0.0
    ndcg_sum = 0.0
    mrr_sum = 0.0
    evidence_found = 0
    evidence_total = 0
    returned_total = 0
    unmapped_total = 0
    negative_total = 0
    isolation_total = 0
    latencies = []
    known_sources = set(records)
    for query in fixture["queries"]:
        top_k = query.get("top_k", args.top_k)
        response = client.request(
            "/search",
            {"query": query["query"], "user_id": qualify_user(run, query["user_id"]), "top_k": top_k},
        )
        latencies.append(response.elapsed_ms)
        body, body_error = decode(response)
        errors = result_errors(body, top_k) if response.status == 200 and body is not None else [body_error or f"status={response.status}"]
        if errors:
            operational_failures += 1
            print(f"FAIL query={query['id']} latency_ms={response.elapsed_ms:.1f} errors={'; '.join(errors)}")
        if not isinstance(body, dict) or not isinstance(body.get("data"), list):
            continue
        data = body["data"]
        if any(not isinstance(result, dict) or not isinstance(result.get("content"), str) for result in data):
            continue
        relevant = set(query.get("relevant", []))
        negative = set(query.get("negative", []))
        ranked_sources = []
        retrieved = set()
        query_unmapped = 0
        query_negative = 0
        query_isolation = 0
        for result in data:
            sources = set(SOURCE_RE.findall(result["content"])) & known_sources
            ranked_sources.append(sources)
            retrieved.update(sources)
            if not sources:
                query_unmapped += 1
            if sources & negative:
                query_negative += 1
            if any(records[source]["user_id"] != query["user_id"] for source in sources):
                query_isolation += 1
        hits = retrieved & relevant
        if relevant:
            positive_queries += 1
            recall_any_sum += float(bool(hits))
            recall_all_sum += float(hits == relevant)
            evidence_found += len(hits)
            evidence_total += len(relevant)
            ranked_seen = set()
            binary_ranks = []
            for sources in ranked_sources:
                new_hits = (sources & relevant) - ranked_seen
                binary_ranks.append(int(bool(new_hits)))
                ranked_seen.update(sources & relevant)
            ideal = dcg([1] * min(len(relevant), top_k))
            ndcg_sum += dcg(binary_ranks) / ideal if ideal else 0.0
            first = next((rank for rank, hit in enumerate(binary_ranks, 1) if hit), None)
            mrr_sum += 1.0 / first if first else 0.0
        returned_total += len(data)
        unmapped_total += query_unmapped
        negative_total += query_negative
        isolation_total += query_isolation
        print(
            f"QUERY id={query['id']} scenario={query['scenario']} latency_ms={response.elapsed_ms:.1f} "
            f"returned={len(data)} relevant={len(hits)}/{len(relevant)} negative={query_negative} "
            f"unmapped={query_unmapped} isolation={query_isolation}"
        )

    denominator = positive_queries or 1
    result_denominator = returned_total or 1
    metrics = {
        f"recall_any@{args.top_k}": recall_any_sum / denominator,
        f"recall_all@{args.top_k}": recall_all_sum / denominator,
        f"evidence_recall@{args.top_k}": evidence_found / evidence_total if evidence_total else 0.0,
        f"nDCG@{args.top_k}": ndcg_sum / denominator,
        "MRR": mrr_sum / denominator,
        "unmapped_rate": unmapped_total / result_denominator,
        "negative_rate": negative_total / result_denominator,
    }
    print("METRICS " + " ".join(f"{key}={value:.4f}" for key, value in metrics.items()))
    if latencies:
        print(
            f"LATENCY count={len(latencies)} mean_ms={statistics.fmean(latencies):.1f} "
            f"p50_ms={percentile(latencies, 0.50):.1f} p95_ms={percentile(latencies, 0.95):.1f}"
        )
    print(
        f"SUMMARY mode=quality operational_failures={operational_failures} isolation_violations={isolation_total} "
        f"unmapped={unmapped_total}/{returned_total} negative={negative_total}/{returned_total}"
    )
    return 1 if operational_failures or isolation_total else 0


def percentile(values: list[float], fraction: float) -> float:
    ordered = sorted(values)
    if not ordered:
        return 0.0
    index = max(0, math.ceil(fraction * len(ordered)) - 1)
    return ordered[index]


def print_phase(name: str, results: list[dict[str, Any]]) -> None:
    statuses = Counter(result["status"] for result in results)
    latencies = [result["latency"] for result in results]
    status_text = ",".join(f"{status}:{count}" for status, count in sorted(statuses.items()))
    print(
        f"PHASE {name} count={len(results)} statuses={status_text} mean_ms={statistics.fmean(latencies):.1f} "
        f"p50_ms={percentile(latencies, 0.50):.1f} p95_ms={percentile(latencies, 0.95):.1f} "
        f"p99_ms={percentile(latencies, 0.99):.1f} max_ms={max(latencies):.1f}"
    )


def load(args: argparse.Namespace, client: Client) -> int:
    if args.adds < 1 or args.searches < 1 or args.concurrency < 1 or args.users < 0:
        print("ERROR --adds, --searches, and --concurrency must be positive; --users cannot be negative", file=sys.stderr)
        return 2
    user_count = args.users or args.adds
    if user_count > args.adds:
        print("ERROR --users cannot exceed --adds", file=sys.stderr)
        return 2
    run = uuid.uuid4().hex[:12]
    users = [f"local-load-{run}-{index}" for index in range(user_count)]

    def add_one(index: int) -> dict[str, Any]:
        user_index = index % user_count
        payload = add_payload(
            f"load-add-{run}-{index}",
            users[user_index],
            "load-session",
            f"load shared retrieval phrase [[AML_LOAD:{user_index}:{index}]]",
            1786838400000 + index,
        )
        response = client.request("/add", payload)
        body, _ = decode(response)
        exact = body == {
            "success": True,
            "request_id": payload["request_id"],
            "user_id": users[user_index],
            "session_id": "load-session",
        }
        return {"status": response.status, "latency": response.elapsed_ms, "schema": exact}

    with concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency) as executor:
        add_results = list(executor.map(add_one, range(args.adds)))
    print_phase("add", add_results)

    def search_one(operation: int) -> dict[str, Any]:
        user_index = operation % user_count
        response = client.request(
            "/search", {"query": "load shared retrieval phrase", "user_id": users[user_index], "top_k": args.top_k}
        )
        body, body_error = decode(response)
        errors = result_errors(body, args.top_k) if response.status == 200 and body is not None else [body_error or "HTTP failure"]
        leak = False
        visible = set()
        if isinstance(body, dict) and isinstance(body.get("data"), list):
            for result in body["data"]:
                if not isinstance(result, dict) or not isinstance(result.get("content"), str):
                    continue
                seen = {
                    (int(user), int(record))
                    for user, record in re.findall(r"\[\[AML_LOAD:(\d+):(\d+)\]\]", result["content"])
                }
                if any(user != user_index for user, _ in seen):
                    leak = True
                visible.update(record for user, record in seen if user == user_index)
        expected = min(sum(index % user_count == user_index for index in range(args.adds)), args.top_k)
        return {
            "status": response.status,
            "latency": response.elapsed_ms,
            "schema": not errors,
            "isolation": not leak,
            "visibility": len(visible) >= expected,
        }

    with concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency) as executor:
        search_results = list(executor.map(search_one, range(args.searches)))
    print_phase("search", search_results)

    add_http_failures = sum(result["status"] != 200 for result in add_results)
    add_schema_failures = sum(result["status"] == 200 and not result["schema"] for result in add_results)
    search_http_failures = sum(result["status"] != 200 for result in search_results)
    search_schema_failures = sum(result["status"] == 200 and not result["schema"] for result in search_results)
    isolation_violations = sum(not result["isolation"] for result in search_results)
    visibility_failures = sum(not result["visibility"] for result in search_results)
    failures = (
        add_http_failures
        + add_schema_failures
        + search_http_failures
        + search_schema_failures
        + isolation_violations
        + visibility_failures
    )
    print(
        f"SUMMARY mode=load add_http_failures={add_http_failures} add_schema_failures={add_schema_failures} "
        f"search_http_failures={search_http_failures} search_schema_failures={search_schema_failures} "
        f"isolation_violations={isolation_violations} visibility_failures={visibility_failures}"
    )
    return 1 if failures else 0


def parser() -> argparse.ArgumentParser:
    default_fixture = str(Path(__file__).resolve().parent.parent / "eval" / "fixtures" / "synthetic.json")
    root = argparse.ArgumentParser(description=__doc__)
    root.add_argument("--base-url", default=os.getenv("AML_BASE_URL", "http://127.0.0.1:8080"))
    root.add_argument("--api-key", default=os.getenv("AML_API_KEY", ""))
    root.add_argument("--timeout", type=float, default=30.0, help="request timeout in seconds")
    subparsers = root.add_subparsers(dest="mode", required=True)

    conformance_parser = subparsers.add_parser("conformance", help="exercise local AML HTTP contract")
    conformance_parser.add_argument(
        "--strict-invalid", action="store_true", help="fail instead of warn when unknown/trailing fields are accepted"
    )

    quality_parser = subparsers.add_parser("quality", help="load and score a native synthetic fixture")
    quality_parser.add_argument("--fixture", default=default_fixture)
    quality_parser.add_argument("--top-k", type=int, default=5)

    load_parser = subparsers.add_parser("load", help="run phased concurrent Add then Search traffic")
    load_parser.add_argument("--adds", type=int, default=20)
    load_parser.add_argument("--searches", type=int, default=40)
    load_parser.add_argument("--concurrency", type=int, default=8)
    load_parser.add_argument("--users", type=int, default=0, help="user scopes; default one user per Add")
    load_parser.add_argument("--top-k", type=int, default=5)
    return root


def main() -> int:
    args = parser().parse_args()
    if args.timeout <= 0:
        print("ERROR --timeout must be positive", file=sys.stderr)
        return 2
    if hasattr(args, "top_k") and not 1 <= args.top_k <= 100:
        print("ERROR --top-k must be between 1 and 100", file=sys.stderr)
        return 2
    print(LABEL)
    print(f"TARGET {args.base_url} mode={args.mode}")
    client = Client(args.base_url, args.api_key, args.timeout)
    return {"conformance": conformance, "quality": quality, "load": load}[args.mode](args, client)


if __name__ == "__main__":
    raise SystemExit(main())
