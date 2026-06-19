# lm-serve-bench

`lm-serve-bench` runs fixed-schedule LLM serving benchmarks against an OpenAI-compatible chat completions endpoint. It materializes workloads from `data/suites/default.json`, invokes AIPerf, and writes a summary.

The default suite lasts 25 minutes. Its mixed `overall` replay sends 242,222 requests.

## Workloads

| Workload | Requests | Trace source | Realistic prompt source |
| --- | ---: | --- | --- |
| `prod-chat` | 116,366 | [Azure LLM Inference Trace 2024](https://raw.githubusercontent.com/Azure/AzurePublicDataset/master/AzureLLMInferenceDataset2024.md) | [ShareGPT](https://huggingface.co/datasets/anon8231489123/ShareGPT_Vicuna_unfiltered), [WildChat](https://huggingface.co/datasets/allenai/WildChat-4.8M) |
| `prod-cache` | 11,219 | [Qwen Bailian usage traces](https://github.com/alibaba-edu/qwen-bailian-usagetraces-anon) | Deterministic renderer keyed by trace `hash_ids` |
| `rag-cache` | 3,307 | [RAGPulse](https://github.com/flashserve/RAGPulse) | [RAGPulse](https://github.com/flashserve/RAGPulse) |
| `code-repo` | 80,000 | [Azure LLM Inference Trace 2024](https://raw.githubusercontent.com/Azure/AzurePublicDataset/master/AzureLLMInferenceDataset2024.md) | [Blazedit](https://huggingface.co/datasets/vdaita/edit_5k_char), [SWE-bench Lite](https://huggingface.co/datasets/SWE-bench/SWE-bench_Lite), [BigCodeBench](https://huggingface.co/datasets/bigcode/bigcodebench-hard) |
| `long-context` | 1,111 | [Mooncake ArXiv production trace](https://github.com/kvcache-ai/Mooncake/tree/main/FAST25-release/arxiv-trace) | [LongBench v2](https://huggingface.co/datasets/THUDM/LongBench-v2), [InfiniteBench](https://github.com/OpenBMB/InfiniteBench), [RULER](https://github.com/NVIDIA/RULER) |
| `agentic-tools` | 20,219 | [tau2-bench](https://github.com/sierra-research/tau2-bench) | [tau-bench](https://github.com/sierra-research/tau-bench), [tau2-bench](https://github.com/sierra-research/tau2-bench) |
| `cache-control` | 10,000 | Synthetic phased arrivals | Synthetic prefix generator |

## Workload Notes

- `prod-chat`: bursty production-style chat traffic with varied input lengths.
- `prod-cache`: production cache traces for prefix reuse, eviction, and memory-pressure behavior.
- `rag-cache`: RAG requests with repeated system prompts, retrieved context, history, and questions.
- `code-repo`: code generation, editing, and repository issue tasks.
- `long-context`: production requests at 16k+ tokens that stress prefill and KV memory, including dense 30k+ and 40k+ tails.
- `agentic-tools`: timed, multi-turn tool-use sessions with idle gaps.
- `cache-control`: deterministic high-sharing and low-sharing cache baselines.

## Trace And Prompt Derivation

Each workload becomes `trace.jsonl`, `pool.jsonl`, `workload.json`, `aiperf.yaml`, and runnable scripts in the output directory.

- `prod-chat` preserves an Azure 25-minute trace window's timestamps and input lengths. ShareGPT and WildChat records fill the prompt pool. No hashes are added.
- `prod-cache` preserves the Qwen trace's timestamps, lengths, and cache-block `hash_ids`, including IDs such as `1089`, `1090`, and `1091`. Those IDs also key the deterministic prompt renderer.
- `rag-cache` preserves a high-variance 25-minute RAGPulse window, including component hashes for system prompts, passages, history, web search, and user input.
- `code-repo` selects a high-variance Azure window, downsamples it to 80,000 requests, and builds a code prompt pool. No hashes are added.
- `long-context` preserves a 1,111-row high-variance 25-minute Mooncake ArXiv production-trace window filtered to 16k+ input tokens. It includes 395 requests at 30k+ and 236 at 40k+, and preserves source cache block `hash_ids`. The prompt pool still uses LongBench, InfiniteBench, and RULER-shaped records for the throughput sweep.
- `agentic-tools` converts timed tau2 assistant calls, repeats their cadence to fill the benchmark duration, and derives hashes from source files and tool names for calls with tools.
- `cache-control` generates timestamps, lengths, and deterministic hashes. Even requests reuse shared prefix IDs such as `8001`, `8002`, and `8003`; the others use unique IDs.

Default replay, load sweeps, and RPS sweeps give AIPerf `trace.jsonl` as `mooncake_trace`: timestamps, `input_length`, and optional `hash_ids`, rather than full prompt text. `pool.jsonl` contains the realistic rendered prompts and is used only by the closed-loop throughput sweep.

With `--workload all` and no sweep, all traces are rescaled to one duration, tagged with `_workload`, sorted by timestamp, and replayed as `overall`.

## Running A Benchmark

1. Load the suite and materialize every workload under `--out` (default: `out/default`).
2. Build the mixed `overall` trace, or select one workload or sweep script.
3. Run `${AIPERF_BIN:-aiperf} profile` with streaming enabled, `temperature:0`, the chosen model, and endpoint.
4. AIPerf writes its artifacts under the workload artifact directory.
5. The benchmark reads AIPerf JSON exports and writes `summary.json` and `summary.md`.

```sh
go run ./cmd/lm-serve-bench run \
  --endpoint http://localhost:8000 \
  --model Qwen/Qwen3-0.6B
```

```sh
# One workload
go run ./cmd/lm-serve-bench run --workload code-repo

# Generate files and scripts without running AIPerf
go run ./cmd/lm-serve-bench run --dry-run

# Built-in OpenAI-compatible mock endpoint
go run ./cmd/lm-serve-bench mock-server --addr 127.0.0.1:8000
```

## Reported Statistics

`summary.json` and `summary.md` contain per-run, per-workload, and overall results. The terminal summary shows completed workloads/runs, total requests, request throughput, output token throughput, and p90 TTFT, request latency, and TPOT.

When AIPerf provides the data, the summary also includes:

- Request count, request throughput, output token count, and output token throughput (`tps`).
- TTFT, request latency, and TPOT in milliseconds: average, min, max, p1, p5, p10, p25, p50, p75, p90, p95, p99, and standard deviation.
- Input and output sequence lengths: average, p50, p95, and p99.
- Result-file paths and an error/status for unavailable or unreadable runs.

For multiple runs, request and token throughputs are summed; latency distribution values are averaged across the successful runs. The `overall` replay also derives a workload breakdown from AIPerf request records when those records are available.

## Features

- Fixed-schedule replay and a mixed `overall` replay.
- Run a single workload with `--workload <id>`.
- Override suite, output directory, model, endpoint, and replay duration with `--suite`, `--out`, `--model`, `--endpoint`, and `--duration`.
- Use `--dry-run` to generate the benchmark without invoking AIPerf.
- Load sweep: `--sweep load` with `0.25x`, `0.50x`, `1x`, `2x`, `4x`, and `8x` trace multipliers.
- Throughput sweep: `--sweep throughput` with closed-loop concurrency from 1 to 256 and the realistic prompt pool.
- RPS ramp: `--sweep rps --target-rps <n> --ramp-steps <n>`; `--target-qps` is an alias.
- Preserved or generated cache hashes, long-context prompt padding, generated AIPerf YAML and scripts, `AIPERF_BIN` and `ARTIFACT_DIR` overrides, and Docker support.

## Output Layout

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
    artifacts/
```

RPS sweeps create `sweep-rps.sh` and `trace-rps-*.jsonl` when run.
