# Benchmark Data

`lm-serve-bench` should run as a benchmark, not as a harness that asks users to assemble their own data.

The default suite in `data/suites/default.json` is the benchmark contract. Its workloads materialize into AIPerf traces and prompt pools deterministically, so two users running the same image against different endpoints send the same workload mix.

The active bundled corpus lives in `data/bundled/`.

`data/bundled/manifest.json` is the source contract for the shipped benchmark corpus.

Files under `data/bundled/traces/` are the benchmark traffic inputs. When public exact traces are bundled, they are high-variance 25-minute windows selected from the upstream trace and replayed without row-count expansion. Source-shaped fixture traces are still expanded deterministically for workload families whose full upstream windows have not been imported yet.

- `prod-chat.jsonl`: high-variance Azure conversation trace window.
- `prod-cache.jsonl`: high-variance Qwen Bailian prefix-cache trace window with repeated block hashes.
- `rag-cache.jsonl`: RAGPulse-style component/session arrivals.
- `code-repo.jsonl`: high-variance Azure code trace window.
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

Normal benchmark users should run `lm-serve-bench run` directly against an endpoint.
