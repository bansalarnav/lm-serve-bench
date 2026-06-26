#!/usr/bin/env python3
"""One-off helper for pairing bundled trace rows with source prompts.

This is intentionally not part of normal benchmark materialization. It fetches
or reads larger prompt sources into rendered candidate JSONL files, then enriches
selected bundled trace JSONL files with deterministic, length-matched input_text.
"""

from __future__ import annotations

import argparse
import bisect
import json
import os
import sys
import time
import urllib.parse
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable


REPO_ROOT = Path(__file__).resolve().parents[1]
TRACE_DIR = REPO_ROOT / "data" / "bundled" / "traces"
PROMPT_DIR = REPO_ROOT / "data" / "bundled" / "prompts"
DEFAULT_CANDIDATE_DIR = REPO_ROOT / "tmp" / "prompt-pairing-candidates"

HF_ROWS_URL = "https://datasets-server.huggingface.co/rows"


@dataclass
class Candidate:
    index: int
    source: str
    text: str
    tokens: int
    uses: int = 0


def main() -> int:
    parser = argparse.ArgumentParser()
    subparsers = parser.add_subparsers(dest="command", required=True)

    fetch = subparsers.add_parser("fetch", help="Fetch/render prompt candidates")
    fetch.add_argument("--out-dir", type=Path, default=DEFAULT_CANDIDATE_DIR)
    fetch.add_argument("--only", action="append", choices=["prod-chat", "code-repo", "rag-cache", "long-context"])
    fetch.add_argument("--include-wildchat", action="store_true")
    fetch.add_argument("--wildchat-max-rows", type=int, default=50_000)
    fetch.add_argument("--wildchat-page-size", type=int, default=100)
    fetch.add_argument("--skip-sharegpt", action="store_true")
    fetch.add_argument("--skip-longbench", action="store_true")
    fetch.add_argument("--longbench-page-size", type=int, default=10)

    pair = subparsers.add_parser("pair", help="Pair candidates into trace files")
    pair.add_argument("--candidate-dir", type=Path, default=DEFAULT_CANDIDATE_DIR)
    pair.add_argument("--write", action="store_true")
    pair.add_argument("--summary", type=Path, default=REPO_ROOT / "tmp" / "prompt-pairing-summary.json")
    pair.add_argument("--workload", action="append", choices=["prod-chat", "code-repo", "rag-cache", "long-context"])
    pair.add_argument("--tolerance-tokens", type=int, default=128)
    pair.add_argument("--tolerance-ratio", type=float, default=0.10)

    args = parser.parse_args()
    if args.command == "fetch":
        fetch_candidates(args)
        return 0
    if args.command == "pair":
        pair_traces(args)
        return 0
    raise AssertionError(args.command)


def fetch_candidates(args: argparse.Namespace) -> None:
    args.out_dir.mkdir(parents=True, exist_ok=True)
    only = set(args.only or ["prod-chat", "code-repo", "rag-cache", "long-context"])

    if "code-repo" in only:
        code_path = args.out_dir / "code-repo.jsonl"
        with code_path.open("w", encoding="utf-8") as out:
            fetch_hf_rows("vdaita/edit_5k_char", "default", "train", "blazedit", render_blazedit, out)
            fetch_hf_rows("SWE-bench/SWE-bench_Lite", "default", "dev", "swebench", render_swebench, out)
            fetch_hf_rows("SWE-bench/SWE-bench_Lite", "default", "test", "swebench", render_swebench, out)
            fetch_hf_rows("bigcode/bigcodebench-hard", "default", "v0.1.4", "bigcodebench", render_bigcodebench, out)
        print(f"wrote {code_path}")

    if "rag-cache" in only:
        rag_path = args.out_dir / "rag-cache.jsonl"
        with rag_path.open("w", encoding="utf-8") as out:
            fetch_ragpulse(out)
            append_existing_prompts("rag-cache", out)
        print(f"wrote {rag_path}")

    if "prod-chat" in only:
        chat_path = args.out_dir / "prod-chat.jsonl"
        with chat_path.open("w", encoding="utf-8") as out:
            if not args.skip_sharegpt:
                fetch_sharegpt(out)
            append_existing_prompts("prod-chat", out)
            if args.include_wildchat:
                fetch_wildchat(out, args.wildchat_max_rows, args.wildchat_page_size)
        print(f"wrote {chat_path}")

    if "long-context" in only:
        long_path = args.out_dir / "long-context.jsonl"
        with long_path.open("w", encoding="utf-8") as out:
            if not args.skip_longbench:
                fetch_hf_rows("zai-org/LongBench-v2", "default", "train", "longbench", render_long_context, out, page_size=args.longbench_page_size)
            append_existing_prompts("long-context", out)
        print(f"wrote {long_path}")


def fetch_hf_rows(
    dataset: str,
    config: str,
    split: str,
    source: str,
    renderer: Callable[[dict[str, Any]], str],
    out: Any,
    page_size: int = 100,
    max_rows: int | None = None,
) -> None:
    offset = 0
    total = None
    written = 0
    while total is None or offset < total:
        if max_rows is not None and offset >= max_rows:
            break
        length = page_size
        if max_rows is not None:
            length = min(length, max_rows - offset)
        payload = hf_rows(dataset, config, split, offset, length)
        rows = payload.get("rows", [])
        total = int(payload.get("num_rows_total", offset + len(rows)))
        if not rows:
            break
        for item in rows:
            text = renderer(item.get("row", {})).strip()
            if text:
                write_candidate(out, source, text, item.get("row_idx", offset))
                written += 1
        offset += len(rows)
        print(f"{source}:{split} {min(offset, total)}/{total}", file=sys.stderr)
    print(f"{source}:{split} wrote {written}", file=sys.stderr)


def hf_rows(dataset: str, config: str, split: str, offset: int, length: int) -> dict[str, Any]:
    params = urllib.parse.urlencode({
        "dataset": dataset,
        "config": config,
        "split": split,
        "offset": str(offset),
        "length": str(length),
    })
    return read_json_url(f"{HF_ROWS_URL}?{params}")


def fetch_sharegpt(out: Any) -> None:
    url = "https://huggingface.co/datasets/anon8231489123/ShareGPT_Vicuna_unfiltered/resolve/main/ShareGPT_V3_unfiltered_cleaned_split_no_imsorry.json"
    data = read_json_url(url)
    if not isinstance(data, list):
        raise ValueError("ShareGPT payload was not a JSON array")
    for i, record in enumerate(data):
        text = render_chat(record, ["conversations", "messages"]).strip()
        if text:
            write_candidate(out, "sharegpt", text, i)
    print(f"sharegpt wrote {len(data)}", file=sys.stderr)


def fetch_wildchat(out: Any, max_rows: int, page_size: int) -> None:
    fetch_hf_rows(
        "allenai/WildChat-4.8M",
        "default",
        "train",
        "wildchat",
        lambda row: render_chat(row, ["conversation", "messages", "turns"]),
        out,
        page_size=page_size,
        max_rows=max_rows,
    )


def fetch_ragpulse(out: Any) -> None:
    base = "https://raw.githubusercontent.com/flashserve/RAGPulse/main/data"
    components = {
        "sys_prompt": read_component_file(f"{base}/1_sys_prompt.jsonl"),
        "passages_ids": read_component_file(f"{base}/2_passages.jsonl"),
        "history": read_component_file(f"{base}/3_history.jsonl"),
        "user_input": read_component_file(f"{base}/4_user_input.jsonl"),
        "web_search": read_component_file(f"{base}/5_web_search.jsonl"),
    }
    trace_url = f"{base}/0_trace.jsonl"
    written = 0
    for i, row in enumerate(read_jsonl_url(trace_url)):
        ids = row.get("hash_ids", {})
        system = join_components(components["sys_prompt"], ids.get("sys_prompt", []))
        passages = join_components(components["passages_ids"], ids.get("passages_ids", []))
        history = join_components(components["history"], ids.get("history", []))
        web = join_components(components["web_search"], ids.get("web_search", []))
        question = join_components(components["user_input"], ids.get("user_input", []))
        parts = []
        if system:
            parts.append(f"System:\n{system}")
        if passages:
            parts.append(f"Retrieved context:\n{passages}")
        if web:
            parts.append(f"Web search:\n{web}")
        if history:
            parts.append(f"Conversation history:\n{history}")
        if question:
            parts.append(f"User question:\n{question}")
        if not parts:
            continue
        parts.append("Answer based on the retrieved context.")
        text = "\n\n".join(parts).strip()
        if text:
            write_candidate(out, "ragpulse", text, i)
            written += 1
    print(f"ragpulse wrote {written}", file=sys.stderr)


def read_component_file(url: str) -> dict[int, str]:
    values: dict[int, str] = {}
    for row in read_jsonl_url(url):
        key_value = first_present(row, ["id", "hash_id", "key", "idx", "index", "sys_prompt_id", "passage_id", "history_id", "user_input_id", "web_search_id"])
        if key_value is None:
            continue
        key = int(key_value)
        text = any_to_text(first_present(row, ["text", "content", "value", "prompt", "passage", "input"]))
        values[key] = text
    return values


def join_components(values: dict[int, str], ids: list[Any]) -> str:
    parts = []
    for raw in ids or []:
        try:
            key = int(raw)
        except (TypeError, ValueError):
            continue
        text = values.get(key, "").strip()
        if text:
            parts.append(text)
    return "\n".join(parts)


def append_existing_prompts(workload: str, out: Any) -> None:
    path = PROMPT_DIR / f"{workload}.jsonl"
    if not path.exists():
        return
    for i, row in enumerate(read_jsonl_file(path)):
        source = str(row.get("_source") or row.get("source_type") or workload)
        text = render_by_source(source, row).strip()
        if text:
            write_candidate(out, source, text, f"bundled-{i}")


def pair_traces(args: argparse.Namespace) -> None:
    workloads = args.workload or ["prod-chat", "code-repo", "rag-cache", "long-context"]
    summary = {}
    for workload in workloads:
        candidates = load_candidates(args.candidate_dir / f"{workload}.jsonl")
        if not candidates:
            raise ValueError(f"no candidates for {workload}")
        trace_path = TRACE_DIR / f"{workload}.jsonl"
        rows = read_jsonl_file(trace_path)
        paired_rows, stats = pair_rows(rows, candidates, args.tolerance_tokens, args.tolerance_ratio)
        summary[workload] = stats
        print_summary(workload, stats)
        if args.write:
            write_jsonl_file(trace_path, paired_rows)
    args.summary.parent.mkdir(parents=True, exist_ok=True)
    args.summary.write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(f"wrote {args.summary}")


def load_candidates(path: Path) -> list[Candidate]:
    seen = set()
    candidates = []
    for row in read_jsonl_file(path):
        text = str(row.get("input_text") or "").strip()
        if not text or text in seen:
            continue
        seen.add(text)
        source = str(row.get("_source") or "unknown")
        tokens = int(row.get("approx_input_tokens") or approximate_tokens(text))
        candidates.append(Candidate(len(candidates), source, text, tokens))
    candidates.sort(key=lambda c: (c.tokens, c.source, c.index))
    return candidates


def pair_rows(
    rows: list[dict[str, Any]],
    candidates: list[Candidate],
    tolerance_tokens: int,
    tolerance_ratio: float,
) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    tokens = [c.tokens for c in candidates]
    source_counts: dict[str, int] = {}
    deltas = []
    output = []
    for row_index, row in enumerate(rows):
        target = int(row.get("input_length") or 0)
        chosen = choose_candidate(candidates, tokens, target, tolerance_tokens, tolerance_ratio)
        chosen.uses += 1
        source_counts[chosen.source] = source_counts.get(chosen.source, 0) + 1
        delta = abs(chosen.tokens - target)
        deltas.append(delta)
        enriched = dict(row)
        enriched["input_text"] = chosen.text
        output.append(enriched)
        if row_index and row_index % 25_000 == 0:
            print(f"paired {row_index}/{len(rows)}", file=sys.stderr)

    use_counts = [c.uses for c in candidates if c.uses > 0]
    return output, {
        "rows": len(rows),
        "candidates": len(candidates),
        "used_candidates": len(use_counts),
        "max_candidate_reuse": max(use_counts) if use_counts else 0,
        "mean_candidate_reuse": round(sum(use_counts) / len(use_counts), 3) if use_counts else 0,
        "delta_min": min(deltas) if deltas else 0,
        "delta_p50": percentile(deltas, 0.50),
        "delta_p90": percentile(deltas, 0.90),
        "delta_p99": percentile(deltas, 0.99),
        "delta_max": max(deltas) if deltas else 0,
        "sources": source_counts,
    }


def choose_candidate(
    candidates: list[Candidate],
    tokens: list[int],
    target: int,
    tolerance_tokens: int,
    tolerance_ratio: float,
) -> Candidate:
    pos = bisect.bisect_left(tokens, target)
    nearest_delta = None
    for idx in [pos - 1, pos]:
        if 0 <= idx < len(candidates):
            delta = abs(candidates[idx].tokens - target)
            nearest_delta = delta if nearest_delta is None else min(nearest_delta, delta)
    if nearest_delta is None:
        return candidates[0]

    limit = nearest_delta + max(tolerance_tokens, int(target * tolerance_ratio))
    left = pos
    while left > 0 and abs(tokens[left - 1] - target) <= limit:
        left -= 1
    right = pos
    while right < len(candidates) and abs(tokens[right] - target) <= limit:
        right += 1

    best = None
    best_key = None
    for candidate in candidates[left:right]:
        delta = abs(candidate.tokens - target)
        key = (candidate.uses, delta, candidate.source, candidate.index)
        if best_key is None or key < best_key:
            best = candidate
            best_key = key
    if best is None:
        best = candidates[min(pos, len(candidates) - 1)]
    return best


def write_candidate(out: Any, source: str, text: str, source_index: Any) -> None:
    row = {
        "_source": source,
        "source_index": source_index,
        "approx_input_tokens": approximate_tokens(text),
        "input_text": text,
    }
    out.write(json.dumps(row, ensure_ascii=False, separators=(",", ":")) + "\n")


def render_by_source(source: str, row: dict[str, Any]) -> str:
    if source in {"sharegpt"}:
        return render_chat(row, ["conversations", "messages"])
    if source in {"wildchat"}:
        return render_chat(row, ["conversation", "messages", "turns"])
    if source in {"swebench", "swe-bench"}:
        return render_swebench(row)
    if source == "bigcodebench":
        return render_bigcodebench(row)
    if source == "blazedit":
        return render_blazedit(row)
    if source in {"longbench", "infinitebench", "ruler"}:
        return render_long_context(row)
    if source == "ragpulse":
        return render_rag(row)
    return render_generic(row)


def render_chat(row: dict[str, Any], keys: list[str]) -> str:
    for key in keys:
        turns = row.get(key)
        if isinstance(turns, list):
            parts = []
            for turn in turns:
                if not isinstance(turn, dict):
                    continue
                role = str(first_present(turn, ["role", "from", "speaker"]) or "message")
                content = any_to_text(first_present(turn, ["content", "value", "text"])).strip()
                if content:
                    parts.append(f"{role}: {content}")
            if parts:
                return "\n\n".join(parts)
    return render_generic(row)


def render_swebench(row: dict[str, Any]) -> str:
    parts = []
    if row.get("repo"):
        parts.append(f"Repository: {row['repo']}")
    if row.get("instance_id"):
        parts.append(f"Instance: {row['instance_id']}")
    if row.get("base_commit"):
        parts.append(f"Base commit: {row['base_commit']}")
    if row.get("problem_statement"):
        parts.append(f"Issue:\n{row['problem_statement']}")
    if row.get("hints_text"):
        parts.append(f"Hints:\n{row['hints_text']}")
    parts.append("Generate a minimal patch that fixes the issue. Return only the patch or the edited code needed.")
    return "\n\n".join(parts)


def render_bigcodebench(row: dict[str, Any]) -> str:
    prompt = any_to_text(first_present(row, ["instruct_prompt", "complete_prompt", "prompt", "canonical_prompt", "description"]))
    parts = []
    if row.get("task_id"):
        parts.append(f"Task: {row['task_id']}")
    if prompt:
        parts.append(prompt)
    if row.get("libs"):
        parts.append(f"Available libraries:\n{row['libs']}")
    return "\n\n".join(parts)


def render_blazedit(row: dict[str, Any]) -> str:
    instruction = any_to_text(first_present(row, ["instruction", "prompt", "edit_instruction", "change_request"]))
    before = any_to_text(first_present(row, ["input", "source", "before", "old_code", "code"]))
    parts = []
    if instruction:
        parts.append(f"Edit instruction:\n{instruction}")
    if before:
        parts.append(f"Code/context:\n{before}")
    parts.append("Return the edited code.")
    return "\n\n".join(parts)


def render_long_context(row: dict[str, Any]) -> str:
    context = any_to_text(first_present(row, ["context", "document", "documents", "passage", "repo"]))
    question = any_to_text(first_present(row, ["question", "input", "query", "prompt"]))
    parts = []
    if context:
        parts.append(f"Context:\n{context}")
    if question:
        parts.append(f"Question:\n{question}")
    choices = []
    for key in ["choice_A", "choice_B", "choice_C", "choice_D"]:
        if row.get(key):
            choices.append(f"{key}: {row[key]}")
    if choices:
        parts.append("\n".join(choices))
    parts.append("Answer using only the provided context.")
    return "\n\n".join(parts)


def render_rag(row: dict[str, Any]) -> str:
    parts = []
    system = any_to_text(first_present(row, ["system", "system_prompt"]))
    docs = any_to_text(first_present(row, ["documents", "retrieved_docs", "context", "passages"]))
    history = any_to_text(first_present(row, ["history", "chat_history", "messages"]))
    question = any_to_text(first_present(row, ["question", "query", "user_input", "input"]))
    if system:
        parts.append(f"System:\n{system}")
    if docs:
        parts.append(f"Retrieved context:\n{docs}")
    if history:
        parts.append(f"Conversation history:\n{history}")
    if question:
        parts.append(f"User question:\n{question}")
    parts.append("Answer based on the retrieved context.")
    return "\n\n".join(parts)


def render_generic(row: dict[str, Any]) -> str:
    value = first_present(row, ["input_text", "text", "prompt", "input", "question", "query", "content"])
    if value is not None:
        return any_to_text(value)
    return json.dumps(row, ensure_ascii=False, sort_keys=True)


def first_present(row: dict[str, Any], keys: list[str]) -> Any:
    for key in keys:
        if key in row and row[key] not in (None, ""):
            return row[key]
    return None


def any_to_text(value: Any) -> str:
    if value is None:
        return ""
    if isinstance(value, str):
        return value
    if isinstance(value, (dict, list)):
        return json.dumps(value, ensure_ascii=False, sort_keys=True)
    return str(value)


def read_json_url(url: str) -> Any:
    req = urllib.request.Request(url, headers={"User-Agent": "lm-serve-bench-prompt-pairing/1"})
    for attempt in range(5):
        try:
            with urllib.request.urlopen(req, timeout=120) as response:
                return json.load(response)
        except Exception:
            if attempt == 4:
                raise
            time.sleep(2**attempt)
    raise AssertionError("unreachable")


def read_jsonl_url(url: str):
    req = urllib.request.Request(url, headers={"User-Agent": "lm-serve-bench-prompt-pairing/1"})
    with urllib.request.urlopen(req, timeout=120) as response:
        for raw in response:
            line = raw.decode("utf-8").strip()
            if line:
                yield json.loads(line)


def read_jsonl_file(path: Path) -> list[dict[str, Any]]:
    rows = []
    with path.open("r", encoding="utf-8") as file:
        for line_number, line in enumerate(file, 1):
            line = line.strip()
            if not line:
                continue
            try:
                rows.append(json.loads(line))
            except json.JSONDecodeError as err:
                raise ValueError(f"{path}:{line_number}: {err}") from err
    return rows


def write_jsonl_file(path: Path, rows: list[dict[str, Any]]) -> None:
    tmp = path.with_suffix(path.suffix + ".tmp")
    with tmp.open("w", encoding="utf-8") as file:
        for row in rows:
            file.write(json.dumps(row, ensure_ascii=False, separators=(",", ":")) + "\n")
    tmp.replace(path)


def approximate_tokens(text: str) -> int:
    return max(1, len(text.split()))


def percentile(values: list[int], q: float) -> int:
    if not values:
        return 0
    ordered = sorted(values)
    index = int((len(ordered) - 1) * q)
    return ordered[index]


def print_summary(workload: str, stats: dict[str, Any]) -> None:
    print(
        f"{workload}: rows={stats['rows']} candidates={stats['candidates']} "
        f"used={stats['used_candidates']} max_reuse={stats['max_candidate_reuse']} "
        f"delta_p50={stats['delta_p50']} delta_p90={stats['delta_p90']} delta_p99={stats['delta_p99']}"
    )


if __name__ == "__main__":
    raise SystemExit(main())
