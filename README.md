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
go run ./cmd/lm-serve-bench list
go run ./cmd/lm-serve-bench validate-data
go run ./cmd/lm-serve-bench prepare -out out/default
go run ./cmd/lm-serve-bench run -endpoint http://localhost:8000 -model Qwen/Qwen3-0.6B -workload cache-control
go run ./cmd/lm-serve-bench run -endpoint http://localhost:8000 -model Qwen/Qwen3-0.6B -workload cache-control -sweep load
go run ./cmd/lm-serve-bench summarize -out out/default
go run ./cmd/lm-serve-bench mock-server -addr 127.0.0.1:18080
go run ./cmd/lm-serve-bench doctor -endpoint http://localhost:8000 -model Qwen/Qwen3-0.6B
go run ./cmd/lm-serve-bench self-test
```

`prepare` writes one AIPerf-ready directory per workload, including `trace.jsonl`, `workload.json`, `aiperf.yaml`, and `run.sh`.
`workload.json` includes the bundled corpus mode, prompt file, record count, and source mix used for that workload.

`run` prepares the suite and then executes AIPerf for one workload or all workloads. Use `-dry-run` to print the generated commands without contacting an endpoint. Use `-sweep load` or `-sweep throughput` for diagnostic sweeps.

`summarize` scans AIPerf JSON exports under the workload directories and writes `summary.json` and `summary.md`.
If a workload has multiple exports, such as a load or throughput sweep, the workload row aggregates successful runs and the individual run metrics stay listed in `summary.json`.

Replay and load sweeps use AIPerf fixed-schedule `mooncake_trace` files. Throughput sweeps use generated `random_pool` data so AIPerf can run closed-loop concurrency without fixed-schedule constraints.

The normal benchmark path uses the bundled workload data generated from the suite definitions. Users should not provide their own traces or prompt pools for comparable benchmark results.
Use `validate-data` to verify that every bundled corpus file renders before packaging or publishing an image.
The standard production trace files are high-variance 25-minute source windows where public exact traces are bundled. Other workload families use deterministic source-shaped traces until their full upstream trace windows are imported.
The bundled source contract lives in `data/bundled/manifest.json`.

`convert` is a maintainer/import utility for refreshing bundled data from upstream public sources. Supported trace sources are `bailian`, `burstgpt`, and `azure`; use `-window 25m -select high-variance` to emit a source-faithful benchmark window. Supported prompt/task sources are `sharegpt`, `wildchat`, `blazedit`, `swebench`, `bigcodebench`, `longbench`, `infinitebench`, `ruler`, `taubench`, `tau2`, and `ragpulse`.

`self-test` starts an in-process mock OpenAI-compatible endpoint, runs the tiny smoke suite through AIPerf, and writes a normal summary. It is the quickest way to verify the whole local harness path.
It needs permission to bind a localhost port for the mock endpoint.

## Docker

```sh
docker build -t lm-serve-bench .
docker run --rm lm-serve-bench list
docker run --rm lm-serve-bench doctor
docker run --rm lm-serve-bench self-test
docker run --rm lm-serve-bench run \
  -endpoint http://host.docker.internal:8000 \
  -model Qwen/Qwen3-0.6B \
  -workload cache-control
docker run --rm \
  -v "$PWD/out:/bench/out" \
  lm-serve-bench run \
    -endpoint http://host.docker.internal:8000 \
    -model Qwen/Qwen3-0.6B
```

See [docs/aiperf.md](/Users/arnav/code/lm-serve-bench/docs/aiperf.md) for the AIPerf artifact layout.
See [docs/workloads.md](/Users/arnav/code/lm-serve-bench/docs/workloads.md) for the workload taxonomy and category-collapse rationale.
