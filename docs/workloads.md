# Workload Taxonomy

The default benchmark keeps seven reported workloads. That is a little more than the absolute minimum, but it avoids hiding distinct infrastructure failure modes behind one aggregate score.

## Default Workloads

- `prod-chat`: bursty production chat arrivals and realistic chat prompts. Stresses autoscaling, queueing, TTFT, TPOT, and general goodput.
- `prod-cache`: Qwen Bailian-style prefix cache replay. Stresses KV cache hit rate, cache admission, eviction, and memory pressure with repeated block hashes.
- `rag-cache`: repeated RAG components, retrieved context, and session history. Stresses prefill cost, repeated context reuse, and cache retention for retrieval-heavy applications.
- `code-repo`: code editing, repository issue solving, and function generation. Stresses longer prompts, code-shaped text, and higher output caps.
- `long-context`: 32k+ context pressure from LongBench/InfiniteBench/RULER-style records. Stresses prefill throughput, KV memory, and effective decode batching.
- `agentic-tools`: tool-heavy sessions with client-side gaps. Stresses cache eviction under idle periods, session retention, and autoscaling behavior for bursty multi-turn workflows.
- `cache-control`: deterministic high-cache and low-cache calibration. Stresses best-case vs worst-case prefix reuse in a way production traces cannot isolate cleanly.

## Collapse Decision

The tempting collapse is `rag-cache` plus `agentic-tools`, because both are stateful. We keep them separate because they stress different systems behavior:

- RAG mostly stresses repeated retrieved context, long-ish prefill, and shared component reuse.
- Agentic traffic mostly stresses inter-call delays, retention across idle gaps, and eviction/scaling policy.

`prod-cache` and `cache-control` also stay separate. `prod-cache` is production-shaped. `cache-control` is a calibration workload with exact reuse ratios.

If a shorter benchmark profile is needed later, the best five-workload profile is:

- `prod-chat`
- `stateful-cache` combining `prod-cache` and `rag-cache`
- `code-repo`
- `long-context`
- `agentic-tools`

The default remains seven because the benchmark's primary purpose is to expose shortcomings, not to minimize the table.
