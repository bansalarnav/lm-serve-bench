# Bundled Benchmark Corpus

This directory contains the repo-shipped corpus used by the default benchmark.

`manifest.json` lists the source contract for the bundled benchmark data. The validator checks this manifest before packaging or running release checks.

Files under `traces/` are benchmark traffic inputs. Standard production traces are source-faithful 25-minute windows selected from public upstream traces. Fixture traces for RAG, long-context, agentic, and cache-control are expanded deterministically during materialization until their full public-source windows are imported.

Files under `prompts/` are source-shaped JSONL records, not user overrides. The materializer renders them into AIPerf `pool.jsonl` automatically for closed-loop throughput sweeps. Fixed-schedule replay primarily uses `input_length` and optional `hash_ids` so large source traces stay compact and model-agnostic. Each row may include `_source` to select the renderer used by the maintainer import path.

Normal benchmark users should not replace these files.
