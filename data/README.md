# Benchmark Data

`lm-serve-bench` should run as a benchmark, not as a harness that asks users to assemble their own data.

The default suite in `data/suites/default.json` is the benchmark contract. Its workloads materialize into AIPerf traces and prompt pools deterministically, so two users running the same image against different endpoints send the same workload mix.

The active bundled corpus lives in `data/bundled/`.

`data/bundled/manifest.json` is the source contract for the shipped benchmark corpus. `lm-serve-bench validate-data` checks that every manifest source is present and renderable.

Files under `data/bundled/traces/` are compact traffic seeds. During materialization, seed traces are expanded deterministically to the workload's `default_requests` count while preserving the seed arrival pattern. Current bundled trace seeds are:

- `prod-chat.jsonl`: BurstGPT/Azure-style bursty production arrivals.
- `prod-cache.jsonl`: Qwen Bailian-style prefix-cache trace with repeated block hashes.
- `rag-cache.jsonl`: RAGPulse-style component/session arrivals.
- `code-repo.jsonl`: Azure/Qwen-coder-style code workload arrivals.
- `long-context.jsonl`: synthetic long-context arrivals at 32k tokens and above.
- `agentic-tools.jsonl`: tau-bench/tau2-style session gaps and repeated session hashes.

Files under `data/bundled/prompts/` are compact source-shaped prompt records:

- `prod-chat.jsonl`: ShareGPT/WildChat-style chat turns.
- `rag-cache.jsonl`: RAGPulse-style system, retrieved context, history, and question records.
- `code-repo.jsonl`: SWE-bench, BigCodeBench, and code-edit records.
- `long-context.jsonl`: LongBench/RULER-style records padded during materialization to the workload target lengths.
- `agentic-tools.jsonl`: tau-bench/tau2-style task, tool, and state records.

`cache-control` uses deterministic generated block/hash data because its purpose is to precisely control prefix reuse and cache shape.

The default suite intentionally keeps seven reported workload categories. RAG and agentic sessions share implementation machinery, but they stay separate because RAG mostly stresses repeated retrieved context and prefill/cache behavior, while agentic traffic stresses inter-call gaps, session retention, and cache eviction under idle periods.

The `convert` command is for maintainers to refresh or inspect candidate bundled data sources. It supports production traces (`bailian`, `burstgpt`, `azure`) and prompt/task datasets (`sharegpt`, `wildchat`, `blazedit`, `swebench`, `bigcodebench`, `longbench`, `infinitebench`, `ruler`, `taubench`, `tau2`, `ragpulse`).

Normal benchmark users should run `lm-serve-bench run` directly against an endpoint.
