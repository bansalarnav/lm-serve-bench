# Thread dataset

Generate and open the HTML report in one go:

```bash
uv run main.py --open
```

Outputs:

- `sanitized/threads.parquet`: event-level metadata from Codex and Claude Code.
- `sanitized/runs.parquet`: one row per source JSONL file/export segment.
- `outputs/` - Directory with plots and HTML report

Important columns in `threads.parquet`:

- `thread_id`: hashed logical thread/session ID.
- `run_id`: hashed source file/export segment ID.
- `run_event_index`: event position inside that source file.
- `event_index`: event position after merging all runs for the same logical thread.
- `row_id`: unique hashed row ID.
- `canonical_event_type`: one of `user_prompt`, `assistant_message`, `tool_call`, `tool_result`, or `other`.
- `is_user_prompt`, `is_assistant_message`, `is_tool_call`, `is_tool_result`: use these for counts so duplicate log representations do not double-count actions.
- `tool_event_stage`: `call`, `result`, or `runtime_end`.

For subagent analysis, group by `agent_id`, `parent_thread_id`, `child_thread_id`, and `is_sidechain`.

## Coding-agent thread analysis

`analyze_threads.py` reads the metadata-only event parquet produced by `gather_session_data.py` and writes analysis tables and plots for the full dataset plus Codex/Claude breakdowns.

Run it from the repository root:

```bash
uv run --project analysis analysis/analyze_threads.py
```

Or use an existing Python environment with pandas, pyarrow, matplotlib, and numpy:

```bash
python3 analysis/analyze_threads.py
```

Custom paths:

```bash
python3 analysis/analyze_threads.py \
  --input analysis/sanitized/threads.parquet \
  --output-dir analysis/output
```

### Outputs

- `summary.csv`: headline values for all sources combined and each source separately.
- `distribution_summary.csv`: count, mean, standard deviation, and p50/p90/p95/p99/max for run and thread metrics.
- `runs.csv`: one auditable row per inferred agent run/turn.
- `threads.csv`: one row per logical thread.
- `model_calls.csv` and `model_usage.csv`: normalized per-call token/cache accounting and model-level aggregates.
- `tool_calls.csv` and `tool_usage.csv`: tool-level events and aggregates.
- `data_quality.csv`: boundary and data-quality checks.
- PNG plots for run duration, tool calls, user messages, tokens, cache ratios, load-versus-duration, top tools, and reasoning tokens versus total output.

### Definitions and caveats

A **run** starts at the provider's model-run prompt and ends at the corresponding terminal event. Codex uses `event_msg.user_message`; this avoids treating user-role abort notifications as new runs. Claude uses canonical user prompts plus bare `user` records, which occur in some main-agent and delegated streams even when no `user.text` block was recorded. Codex has explicit `task_complete`/`turn_aborted` events and an authoritative `duration_ms` on many runs. Claude has no explicit terminal event in this parquet, so its end is inferred from the last assistant event before the next prompt. Incomplete runs remain in the table with `status=incomplete`.

The parquet's `run_id` identifies a source file/export segment, so the analyzer preserves it as `export_run_id`. Its own conversational run identifier remains `run_id`; `runs.csv` lists all contributing source IDs in `export_run_ids`.

Some imported/resumed histories stamp a whole block of old events within a few milliseconds. When such a run has model/tool activity but a timestamp-derived duration of 100 ms or less, `duration_quality` is set to `collapsed_timestamps` and `duration_seconds` is left blank. The original observation remains in `raw_duration_seconds` for auditing and is excluded from duration summaries.

Delegated/subagent prompts are valid agent runs from a serving perspective and are included by default. They are marked with `is_sidechain`; filter `runs.csv` with `is_sidechain == False` for only top-level user interactions. Claude parallel agents share a logical `thread_id`, so `agent_id` is used to keep their event streams separate.

`user_messages` counts the sanitizer's canonical `is_user_prompt` event for each provider. It does not count tool results or duplicate provider-specific message representations. `top_level_user_messages` excludes sidechain prompts. This can differ from the number of inferred runs because protocol-level aborts and delegated prompts have different serving semantics.

`tool_calls` counts request-side tool events only, not both the request and result. Call IDs are deduplicated when available.

`served_input_tokens` is normalized for systems analysis:

- Codex: `input_tokens`, which already includes cached input.
- Claude: uncached input + cache-read input + cache-creation input.

Codex emits repeated token snapshots. The script calculates increments from cumulative usage and discards zero-delta repeats. Cache ratios are ratios of token sums: `cached_input_tokens / served_input_tokens`. Claude cache writes are reported separately as `cache_creation_ratio`.

Reasoning tokens are a subset of total output tokens. `reasoning_share_of_output` is `reasoning_output_tokens / output_tokens`, and `non_reasoning_output_tokens` is the remainder. This parquet only exposes separate reasoning-token counts for Codex; Claude values remain blank and `reasoning_tokens_available` is false rather than incorrectly treating missing data as zero.

`end_to_end_output_tokens_per_second` includes reasoning, tool execution, and orchestration time. It is useful as a session-level efficiency measure, but it is not model decode throughput.

## Offline HTML report

`main.py` runs the session-data gatherer and analyzer together, then creates a local dashboard with the charts, headline tables, model cost estimates, and data-quality checks:

```bash
uv run --project analysis analysis/main.py --open
```

The report is written to `analysis/report/index.html` by default and can be opened on Windows, macOS, or Linux. During generation, the script downloads the current pricing catalog from [models.dev](https://models.dev) and caches it under `report/data/`. If the refresh fails, it uses the last cached catalog; if no catalog is available, the report still builds with costs marked unavailable.

Custom inputs and output directory:

```bash
uv run --project analysis analysis/main.py \
  --codex-input /path/to/codex/sessions \
  --claude-input /path/to/claude/projects \
  --output-dir /path/to/report
```

Costs use the standard API rates per million tokens for uncached input, cache reads, cache writes, output, and separately-priced reasoning when available. They are estimates and do not include subscriptions, discounts, batch pricing, or long-context pricing tiers. Internal or synthetic model IDs that do not appear in models.dev are shown as unmatched rather than assigned a guessed cost. The report remains metadata-only and does not include transcript contents.
