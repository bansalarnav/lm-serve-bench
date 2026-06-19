# lm-serve-bench

`lm-serve-bench` is a benchmark for systems serving a single LLM.

The project separates:

- bundled traffic traces: arrivals, sessions, cache shape, tool delays, and input lengths
- bundled prompt sources: the actual text rendered into requests
- AIPerf harness exports: the concrete files used to issue traffic and collect metrics

The first implementation targets AIPerf because it already supports fixed-schedule replay, request-rate and concurrency modes, custom datasets, public trace formats, prefix synthesis, raw payload replay, and headless reports.

## Current default suite

- `prod-chat`: Azure production conversation trace window, with ShareGPT/WildChat prompt pools for throughput sweeps
- `prod-cache`: Qwen Bailian cache block replay with real `hash_ids`
- `rag-cache`: RAGPulse-style repeated retrieved-context sessions
- `code-repo`: Azure production code trace window, with Blazedit, SWE-bench, and BigCodeBench prompt pools
- `long-context`: LongBench, InfiniteBench, and RULER-style long prompts for 32k+ contexts
- `agentic-tools`: tau-bench/tau2-bench-style tool-heavy sessions with client-side gaps
- `cache-control`: synthetic high-cache and low-cache calibration controls

## Usage

```sh
go run ./cmd/lm-serve-bench run --endpoint http://localhost:8000 --model Qwen/Qwen3-0.6B --workload all
go run ./cmd/lm-serve-bench run --endpoint http://localhost:8000 --model Qwen/Qwen3-0.6B --workload cache-control
go run ./cmd/lm-serve-bench run --endpoint http://localhost:8000 --model Qwen/Qwen3-0.6B --workload cache-control --duration 10m
go run ./cmd/lm-serve-bench run --endpoint http://localhost:8000 --model Qwen/Qwen3-0.6B --workload cache-control --sweep load
go run ./cmd/lm-serve-bench run --endpoint http://localhost:8000 --model Qwen/Qwen3-0.6B --workload cache-control --sweep rps --target-rps 100 --ramp-steps 5
go run ./cmd/lm-serve-bench mock-server --addr 127.0.0.1:18080
```

`run` prepares the suite, executes AIPerf, writes `summary.json` / `summary.md`, and prints terminal tables plus sweep graphs. The default `--workload all` mode builds one mixed `overall` trace that interleaves all workload categories over the benchmark window. The summary includes both aggregate `overall` metrics and per-workload metrics derived from the mixed run. Use `--duration 10m` to stretch or compress the default replay duration while preserving request order, input lengths, and cache hashes. Use `--dry-run` to print the generated commands without contacting an endpoint. Use `--sweep load`, `--sweep throughput`, or `--sweep rps --target-rps N` for diagnostic sweeps.

If a workload has multiple exports, such as a load or throughput sweep, the workload row aggregates successful runs and the individual run metrics stay listed in `summary.json`. The terminal report also prints compact ASCII charts for sweep runs.

Replay, load sweeps, and RPS ramps use AIPerf fixed-schedule `mooncake_trace` files. The RPS ramp generates scaled trace stages that gradually increase to the requested target while preserving request ordering, input lengths, and cache hashes. Throughput sweeps use generated `random_pool` data so AIPerf can run closed-loop concurrency without fixed-schedule constraints.

The normal benchmark path uses the bundled workload data generated from the suite definitions. Users should not provide their own traces or prompt pools for comparable benchmark results.
The standard production trace files are high-variance 25-minute source windows where public exact traces are bundled. Other workload families use deterministic source-shaped traces until their full upstream trace windows are imported.
The bundled source contract lives in `data/bundled/manifest.json`.

`mock-server` starts a small OpenAI-compatible endpoint for local smoke checks. It needs permission to bind a localhost port.

## Docker

```sh
docker build -t lm-serve-bench .
docker run --rm lm-serve-bench run \
  --endpoint http://host.docker.internal:8000 \
  --model Qwen/Qwen3-0.6B \
  --workload cache-control
docker run --rm \
  -v "$PWD/out:/bench/out" \
  lm-serve-bench run \
    --endpoint http://host.docker.internal:8000 \
    --model Qwen/Qwen3-0.6B
```

See [docs/aiperf.md](/Users/arnav/code/lm-serve-bench/docs/aiperf.md) for the AIPerf artifact layout.
See [docs/workloads.md](/Users/arnav/code/lm-serve-bench/docs/workloads.md) for the workload taxonomy and category-collapse rationale.
