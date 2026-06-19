# lm-serve-bench

`lm-serve-bench` prepares and runs a fixed-schedule LLM serving benchmark against an OpenAI-compatible chat completions endpoint. It uses the bundled suite definition in `data/suites/default.json`, materializes AIPerf-compatible workload directories, runs AIPerf, and writes a compact benchmark summary.

## Workloads

The default suite runs for 25 minutes unless overridden. Running every workload as the mixed `overall` replay sends 241,825 requests.

| Workload | Requests | Trace source | Realistic prompt source |
| --- | ---: | --- | --- |
| `prod-chat` | 116,366 | [Azure LLM Inference Trace 2024](https://raw.githubusercontent.com/Azure/AzurePublicDataset/master/AzureLLMInferenceDataset2024.md) | [ShareGPT](https://huggingface.co/datasets/anon8231489123/ShareGPT_Vicuna_unfiltered), [WildChat](https://huggingface.co/datasets/allenai/WildChat-4.8M) |
| `prod-cache` | 11,219 | [Qwen Bailian usage traces](https://github.com/alibaba-edu/qwen-bailian-usagetraces-anon) | Deterministic block renderer keyed by trace `hash_ids` |
| `rag-cache` | 8,000 | [RAGPulse](https://github.com/flashserve/RAGPulse) | [RAGPulse](https://github.com/flashserve/RAGPulse) |
| `code-repo` | 80,000 | [Azure LLM Inference Trace 2024](https://raw.githubusercontent.com/Azure/AzurePublicDataset/master/AzureLLMInferenceDataset2024.md) | [Blazedit](https://huggingface.co/datasets/vdaita/edit_5k_char), [SWE-bench Lite](https://huggingface.co/datasets/SWE-bench/SWE-bench_Lite), [BigCodeBench](https://huggingface.co/datasets/bigcode/bigcodebench-hard) |
| `long-context` | 240 | Synthetic long-context arrivals | [LongBench v2](https://huggingface.co/datasets/THUDM/LongBench-v2), [InfiniteBench](https://github.com/OpenBMB/InfiniteBench), [RULER](https://github.com/NVIDIA/RULER) |
| `agentic-tools` | 16,000 | [tau2-bench](https://github.com/sierra-research/tau2-bench), [tau-bench historical trajectories](https://github.com/sierra-research/tau-bench) | [tau-bench](https://github.com/sierra-research/tau-bench), [tau2-bench](https://github.com/sierra-research/tau2-bench) |
| `cache-control` | 10,000 | Synthetic phased arrivals | Synthetic prefix generator |

## Workload Descriptions

`prod-chat` represents bursty production chat traffic. It combines real arrival timing and input-length variation with rendered chat prompts, and is mainly useful for measuring queueing behavior, autoscaling behavior, TTFT, TPOT, and goodput under normal assistant-style traffic.

`prod-cache` focuses on production prefix-cache behavior. It preserves Qwen Bailian cache block `hash_ids`, so the replay can exercise repeated prefixes, cache hits, cache eviction, memory pressure, and the latency impact of cache reuse.

`rag-cache` models retrieval-augmented generation sessions. Requests include repeated system prompts, retrieved context, conversation history, and user questions, which makes it useful for measuring prefix/KV retention, RAG context prefill cost, and cache behavior across repeated components.

`code-repo` covers code generation, code editing, and repository issue-solving prompts. It stresses longer prefill, larger prompt bodies, decode throughput, and latency for code-heavy workloads.

`long-context` stresses prefill and KV memory with prompts at roughly 32k tokens and above. It is intentionally much smaller by request count because each request is large.

`agentic-tools` represents multi-turn tool-use sessions. It replays tool-agent style calls separated by client-side gaps and repeated state/tool/task context, which makes it useful for testing KV retention, cache eviction, session latency, and autoscaling under idle gaps.

`cache-control` is a deterministic calibration workload. It creates high-cache-sharing and low-cache-sharing request patterns so cache behavior can be compared against a controlled best-case/worst-case style baseline.

## Trace Derivation

Each workload is materialized into `trace.jsonl`, `pool.jsonl`, `workload.json`, `aiperf.yaml`, and runnable shell scripts under the output directory.

`prod-chat` uses a bundled Azure trace file with 116,366 rows. The materializer keeps the selected source-window timestamps and input lengths, then renders chat prompts from the bundled ShareGPT/WildChat records into the request pool. It does not add `hash_ids`.

`prod-cache` uses a bundled Qwen Bailian trace file with 11,219 rows. The replay preserves timestamps, input lengths, and source cache block `hash_ids`; those IDs are also what make repeated prefix blocks repeat in the rendered prompt text. Example hash IDs include `1089`, `1090`, `1091`, and longer per-block IDs from the bundled trace.

`rag-cache` starts from 6 bundled RAGPulse trace rows and 3 bundled prompt records. The trace is repeated until it reaches 8,000 requests, then scaled across the workload duration. Its `hash_ids` are derived during materialization from prompt components: `rag:system`, `rag:retrieved`, `rag:history`, and `rag:question`. The stable ID algorithm is FNV-1a over the component namespace plus trimmed component text. Example derived IDs include `420860683`, `530162297`, `157273401`, and `1321135449`.

`code-repo` uses a bundled Azure code trace file with 131,765 rows. The materializer selects the high-variance 25-minute source window, downsamples it to 80,000 requests, and renders code prompts from SWE-bench, BigCodeBench, and Blazedit records. It does not add `hash_ids`.

`long-context` starts from 4 bundled synthetic long-context trace rows and 4 prompt records. It repeats the rows to 240 requests, scales them across the workload duration, and pads rendered long-context prompts to the requested approximate input lengths. It does not add `hash_ids`.

`agentic-tools` starts from 6 bundled tau-bench/tau2 trace rows and 3 bundled prompt records. The trace is repeated to 16,000 requests and scaled across the workload duration. Its `hash_ids` are derived from `agentic:task`, `agentic:tools`, and `agentic:state` prompt components with the same stable FNV-1a hash function. Example derived IDs include `1909052184`, `2039932256`, and `850464974`.

`cache-control` is generated deterministically when no bundled trace exists. It creates synthetic timestamps, input lengths, and cache-sharing `hash_ids`; even-numbered requests reuse shared prefix IDs such as `8001`, `8002`, and `8003`, while other requests use unique suffix-style IDs.

When `--workload all` is used without a sweep, the benchmark creates an `overall` mixed replay. It rescales every materialized workload trace to the same replay duration, tags each row with `_workload`, sorts all rows by timestamp, and runs that single interleaved trace.

## What Happens When You Run It

1. `lm-serve-bench run` loads the suite JSON from `--suite`, defaulting to `data/suites/default.json`.
2. It materializes every suite workload into `--out`, defaulting to `out/default`.
3. Materialization writes `workload.json`, `trace.jsonl`, `pool.jsonl`, `aiperf.yaml`, `run.sh`, `sweep-load.sh`, and `sweep-throughput.sh` for each workload. Load sweep trace files are precomputed from the base trace.
4. If `--workload all` and no sweep are selected, it creates `overall/trace.jsonl` by interleaving all workload traces and runs only `overall/run.sh`.
5. If a single workload is selected, it runs that workload's script. If `--workload all` is used with a sweep, it runs the chosen sweep script once per workload.
6. The script invokes `${AIPERF_BIN:-aiperf} profile` with chat streaming enabled, `temperature:0`, the configured model, and the configured endpoint.
7. AIPerf writes its artifacts under each workload's artifact directory.
8. `lm-serve-bench` scans for AIPerf JSON exports, computes request counts, throughput, token throughput, TTFT, latency, TPOT, and sequence-length summaries, then writes `summary.json` and `summary.md`.
9. A terminal summary is printed after successful non-dry-run execution.

## Usage

```sh
go run ./cmd/lm-serve-bench run \
  --endpoint http://localhost:8000 \
  --model Qwen/Qwen3-0.6B
```

Run one workload:

```sh
go run ./cmd/lm-serve-bench run --workload code-repo
```

Print the generated scripts without running AIPerf:

```sh
go run ./cmd/lm-serve-bench run --dry-run
```

Start the built-in OpenAI-compatible mock endpoint:

```sh
go run ./cmd/lm-serve-bench mock-server --addr 127.0.0.1:8000
```

The mock server implements `/v1/chat/completions` and `/health`. It supports streaming and a configurable streamed token delay with `--token-delay`.

## Features

- Fixed-schedule trace replay through AIPerf's `mooncake_trace` input format.
- Mixed `overall` replay that interleaves all default workloads into one benchmark run.
- Single-workload execution with `--workload <id>`.
- Custom suite path with `--suite`.
- Custom output directory with `--out`.
- Custom model name with `--model`.
- Custom OpenAI-compatible endpoint with `--endpoint`.
- Duration override for default trace replay with `--duration`, for example `--duration 10m` or `--duration 90s`.
- Dry runs with `--dry-run`.
- Load sweeps with `--sweep load`, using multipliers `0.25x`, `0.50x`, `1.00x`, `2.00x`, `4.00x`, and `8.00x`.
- Throughput sweeps with `--sweep throughput`, using closed-loop concurrency levels `1`, `2`, `4`, `8`, `16`, `32`, `64`, `128`, and `256`.
- RPS/QPS ramps with `--sweep rps --target-rps <n> --ramp-steps <n>`. `--target-qps` is accepted as an alias for `--target-rps`.
- Random-pool throughput mode through generated `pool.jsonl` files.
- Cache-aware traces with preserved or derived `hash_ids`.
- Long-context prompt padding for long-context workloads.
- Generated `aiperf.yaml` files for direct AIPerf use.
- Generated runnable shell scripts for default replay and sweeps.
- AIPerf binary override with `AIPERF_BIN`.
- Artifact directory override with `ARTIFACT_DIR`.
- Docker image support; the Dockerfile builds the Go binary and installs `aiperf`.

## Outputs

For the default output directory, expect:

```text
out/default/
  suite.json
  summary.json
  summary.md
  <workload>/
    workload.json
    trace.jsonl
    pool.jsonl
    aiperf.yaml
    run.sh
    sweep-load.sh
    sweep-throughput.sh
    trace-load-*.jsonl
    artifacts/
```

RPS sweeps also generate `sweep-rps.sh` and `trace-rps-*.jsonl` at run time.
