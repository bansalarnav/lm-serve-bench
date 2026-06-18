# AIPerf integration

`lm-serve-bench` is the benchmark. It uses AIPerf as the execution engine.

The project generates one AIPerf-ready directory per workload:

- `workload.json`: source metadata and benchmark intent
- `trace.jsonl`: fixed-schedule request trace in AIPerf `mooncake_trace` format
- `pool.jsonl`: random-pool prompt data for closed-loop throughput sweeps
- `aiperf.yaml`: human-readable AIPerf config draft
- `run.sh`: concrete AIPerf command used by `lm-serve-bench run`
- `sweep-load.sh`: runs time-compressed fixed-schedule traces
- `sweep-throughput.sh`: runs closed-loop concurrency sweeps
- `sweep-rps.sh`: generated at run time for `--sweep rps --target-rps N`
- `trace-load-*.jsonl`: generated fixed-schedule traces with timestamps scaled by load multiplier

The trace format intentionally avoids per-request target output lengths. Output length is capped at the workload level with AIPerf's `--osl` option so runs finish predictably without pretending that different models should produce exactly the same number of tokens.

`workload.json` also includes bundled corpus metadata: whether the workload uses trace records, prompt records, or deterministic generated data; trace record counts; materialized request count; file paths; and source mix.

Replay runs, load sweeps, and RPS ramps use `mooncake_trace` fixed schedules. Throughput sweeps use `random_pool` data because AIPerf requires fixed-schedule traces to run with sequential sampling.

## Run locally

```sh
go run ./cmd/lm-serve-bench doctor
go run ./cmd/lm-serve-bench validate-data
go run ./cmd/lm-serve-bench prepare --out out/default
go run ./cmd/lm-serve-bench run \
  --endpoint http://localhost:8000 \
  --model Qwen/Qwen3-0.6B \
  --workload all

go run ./cmd/lm-serve-bench run \
  --endpoint http://localhost:8000 \
  --model Qwen/Qwen3-0.6B \
  --workload cache-control

go run ./cmd/lm-serve-bench run \
  --endpoint http://localhost:8000 \
  --model Qwen/Qwen3-0.6B \
  --workload cache-control \
  --duration 10m

go run ./cmd/lm-serve-bench run \
  --endpoint http://localhost:8000 \
  --model Qwen/Qwen3-0.6B \
  --workload cache-control \
  --sweep load

go run ./cmd/lm-serve-bench run \
  --endpoint http://localhost:8000 \
  --model Qwen/Qwen3-0.6B \
  --workload cache-control \
  --sweep rps \
  --target-rps 100 \
  --ramp-steps 5

go run ./cmd/lm-serve-bench summarize --out out/default
```

`--workload all` is the aggregate benchmark path. It builds one mixed `overall` trace by interleaving all workload traces over the same replay window, then runs that trace once. The summary includes aggregate `overall` metrics plus per-workload metrics derived from AIPerf's request-level export and the `_workload` tags in `overall/trace.jsonl`. Key metrics include request throughput, TPS (`tps`, output tokens/sec), TPOT (`tpot_*_ms`, from inter-token latency), TTFT, request latency, and input/output sequence lengths.

`--duration` applies only to default fixed trace replay. With `--workload all`, it sets the total mixed replay duration. With a single workload, it scales that workload's prepared `trace.jsonl` to the requested wall-clock duration while keeping the same requests, order, input lengths, and cache hashes.

The RPS/QPS ramp is trace-preserving. It computes the workload's baseline request rate from `trace.jsonl`, generates `ramp-steps` scaled traces, and runs them from `target-rps / ramp-steps` up to `target-rps`. `--target-qps` is accepted as an alias for `--target-rps`.

## Smoke test locally

The `smoke` suite is intentionally tiny and uses the same AIPerf fixed-schedule path as the default suite.

The fastest full-stack check is:

```sh
go run ./cmd/lm-serve-bench self-test
```

It starts a mock OpenAI-compatible endpoint in-process, runs AIPerf against it, and writes `summary.json` / `summary.md`.
Because it binds a local HTTP server, sandboxed environments must allow localhost listening.

Start the mock OpenAI-compatible endpoint:

```sh
go run ./cmd/lm-serve-bench mock-server --addr 127.0.0.1:18080
```

In another terminal, run the smoke benchmark:

```sh
go run ./cmd/lm-serve-bench doctor \
  --endpoint http://127.0.0.1:18080 \
  --model mock

go run ./cmd/lm-serve-bench run \
  --suite data/suites/smoke.json \
  --out out/smoke-run \
  --workload smoke-cache-control \
  --endpoint http://127.0.0.1:18080 \
  --model mock
```

If AIPerf is installed somewhere other than `PATH`, set `AIPERF_BIN`. For example:

```sh
AIPERF_BIN="env PYTHONPATH=$PWD/.venv/lib/python3.13/site-packages python3 -m aiperf" \
go run ./cmd/lm-serve-bench run \
  --suite data/suites/smoke.json \
  --out out/smoke-run \
  --workload smoke-cache-control \
  --endpoint http://127.0.0.1:18080 \
  --model mock
```

The smoke suite also verifies both sweep modes:

```sh
AIPERF_BIN="env PYTHONPATH=$PWD/.venv/lib/python3.13/site-packages python3 -m aiperf" \
go run ./cmd/lm-serve-bench run \
  --suite data/suites/smoke.json \
  --out out/smoke-load \
  --workload smoke-cache-control \
  --endpoint http://127.0.0.1:18080 \
  --model mock \
  --sweep load

AIPERF_BIN="env PYTHONPATH=$PWD/.venv/lib/python3.13/site-packages python3 -m aiperf" \
go run ./cmd/lm-serve-bench run \
  --suite data/suites/smoke.json \
  --out out/smoke-throughput \
  --workload smoke-cache-control \
  --endpoint http://127.0.0.1:18080 \
  --model mock \
  --sweep throughput
```

## Bundled data and source imports

The normal benchmark path uses bundled deterministic workload data generated from the suite. Users should not provide their own traces or prompt pools for comparable benchmark results.
Source-window traces are replayed exactly as bundled. Fixture traces are expanded deterministically to the workload request count before AIPerf runs.

Validate the bundled corpus with:

```sh
go run ./cmd/lm-serve-bench validate-data
```

The `convert` command is a maintainer/import utility for refreshing bundled data from upstream public sources. Production traces can be converted into the same AIPerf fixed-schedule format:

```sh
go run ./cmd/lm-serve-bench convert \
  --source bailian \
  --input qwen_traceA_blksz_16.jsonl \
  --output out/qwen_traceA.jsonl \
  --window 25m \
  --select high-variance

go run ./cmd/lm-serve-bench convert \
  --source burstgpt \
  --input BurstGPT.csv \
  --output out/burstgpt.jsonl \
  --limit 10000

go run ./cmd/lm-serve-bench convert \
  --source azure \
  --input AzureLLMInferenceTrace.csv \
  --output out/azure.jsonl \
  --window 25m \
  --select high-variance
```

Converters preserve timing and input length. `--window` emits a contiguous wall-clock slice, and `--select high-variance` chooses the slice with dense traffic, varied input lengths, and bursty gaps. Bailian conversion also preserves `hash_ids`. Per-request source output lengths are intentionally not emitted as AIPerf `output_length`.

Prompt/task datasets can also be rendered into AIPerf JSONL:

```sh
go run ./cmd/lm-serve-bench convert \
  --source swebench \
  --input swebench_lite.jsonl \
  --output data/imports/code-repo-swebench.jsonl \
  --limit 1000

go run ./cmd/lm-serve-bench convert \
  --source longbench \
  --input longbench_v2.jsonl \
  --output data/imports/long-context-longbench.jsonl \
  --limit 200
```

Supported import sources are `bailian`, `burstgpt`, `azure`, `sharegpt`, `wildchat`, `blazedit`, `swebench`, `bigcodebench`, `longbench`, `infinitebench`, `ruler`, `taubench`, `tau2`, and `ragpulse`.

## Summaries

`summarize` reads all AIPerf JSON exports below each workload directory. A normal replay usually has one export. Load and throughput sweeps have multiple exports, so the workload-level metrics aggregate successful runs while `summary.json` keeps every individual run and result path.

## Run from Docker

```sh
docker build -t lm-serve-bench .
docker run --rm \
  -e INFERENCE_URL=http://host.docker.internal:8000 \
  -e MODEL_NAME=Qwen/Qwen3-0.6B \
  lm-serve-bench run --endpoint http://host.docker.internal:8000 --model Qwen/Qwen3-0.6B
```

Use `--dry-run` to verify the generated AIPerf commands without contacting an endpoint:

```sh
docker run --rm lm-serve-bench run --dry-run
docker run --rm lm-serve-bench run --sweep throughput --dry-run
```
