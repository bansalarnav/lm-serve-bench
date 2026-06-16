# Bundled Benchmark Corpus

This directory contains the repo-shipped corpus used by the default benchmark.

`manifest.json` lists the source contract for the bundled benchmark data. The validator checks this manifest before packaging or running release checks.

Files under `traces/` are source-shaped traffic seeds. The materializer expands them to the workload request count and then pairs them with bundled prompt records when that workload has prompts.

Files under `prompts/` are source-shaped JSONL records, not user overrides. The materializer renders them into AIPerf `trace.jsonl` and `pool.jsonl` automatically. Each row may include `_source` to select the renderer used by the maintainer import path.

The current files are compact seed corpora that keep the benchmark self-contained while the larger curated public-source snapshots are prepared. `long-context` records are intentionally expanded during materialization to preserve the target long-context input lengths. Normal benchmark users should not replace these files.
