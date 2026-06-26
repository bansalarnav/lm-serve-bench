# Bundled Benchmark Corpus

This directory contains the repo-shipped corpus used by the default benchmark.

`manifest.json` lists the source contract for the bundled benchmark data.

Files under `traces/` are benchmark traffic inputs. Standard production traces are source-faithful 25-minute windows selected from public upstream traces. Where source prompt text is available, rows include deterministic `input_text` paired by approximate input-token length with reuse spread across the available prompt set. Fixture traces for RAG, long-context, agentic, and cache-control are expanded deterministically during materialization until their full public-source windows are imported.

Files under `prompts/` are source-shaped JSONL records, not user overrides. The materializer renders them into AIPerf `pool.jsonl` automatically for closed-loop throughput sweeps. Fixed-schedule replay uses trace-row `input_text` when present, otherwise `input_length` and optional `hash_ids`. Each row may include `_source` to select the renderer.

Normal benchmark users should not replace these files.
