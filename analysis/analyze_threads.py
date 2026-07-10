import argparse
from pathlib import Path

import matplotlib.pyplot as plt
import numpy as np
import pandas as pd


USER_EVENT_TYPES = {
    "codex": {"event_msg.user_message"},
    "claude": {"user", "user.text"},
}


def safe_ratio(numerator: pd.Series | float, denominator: pd.Series | float):
    return numerator / denominator.replace(0, np.nan) if isinstance(denominator, pd.Series) else (
        numerator / denominator if denominator else np.nan
    )


def percentile(series: pd.Series, value: float) -> float:
    values = series.dropna()
    return values.quantile(value) if not values.empty else np.nan


def load_events(path: Path) -> pd.DataFrame:
    events = pd.read_parquet(path)
    required = {
        "thread_id",
        "source",
        "event_index",
        "timestamp",
        "event_type",
        "role",
        "agent_id",
        "is_sidechain",
        "tool_call_id",
        "tool_name",
        "success",
        "status",
        "duration_ms",
        "time_to_first_token_ms",
        "input_tokens",
        "output_tokens",
        "cached_input_tokens",
        "cache_creation_input_tokens",
        "reasoning_output_tokens",
        "cumulative_input_tokens",
        "cumulative_output_tokens",
        "cumulative_cached_input_tokens",
        "cumulative_reasoning_output_tokens",
        "model",
    }
    missing = required - set(events.columns)
    if missing:
        raise ValueError(f"Parquet is missing required columns: {sorted(missing)}")

    events = events.copy()
    if "run_id" in events.columns:
        events = events.rename(columns={"run_id": "export_run_id"})
    else:
        events["export_run_id"] = pd.NA
    events["timestamp"] = pd.to_datetime(events["timestamp"], utc=True, errors="coerce")
    events["is_sidechain"] = events["is_sidechain"].eq(True)

    # Claude can store the main agent and several parallel subagents under one
    # thread_id. Treat each agent_id as a separate event stream for turn boundaries.
    events["stream_id"] = "main"
    claude_agents = events["source"].eq("claude") & events["agent_id"].notna()
    events.loc[claude_agents, "stream_id"] = "agent:" + events.loc[claude_agents, "agent_id"].astype(str)

    if "is_user_prompt" in events.columns:
        events["is_user_prompt"] = events["is_user_prompt"].eq(True)
    else:
        events["is_user_prompt"] = False
        for source, event_types in USER_EVENT_TYPES.items():
            mask = events["source"].eq(source) & events["event_type"].isin(event_types)
            if source == "claude":
                mask &= events["role"].eq("user")
            events.loc[mask, "is_user_prompt"] = True
    events["is_top_level_user_prompt"] = events["is_user_prompt"] & ~events["is_sidechain"]

    # Run boundaries are stricter than all canonical user-role messages. For
    # example, Codex writes an abort notification as a user-role response item,
    # but it does not start another model run.
    events["is_run_start"] = (
        events["source"].eq("codex") & events["event_type"].eq("event_msg.user_message")
    ) | (
        events["source"].eq("claude")
        & (
            events["is_user_prompt"]
            | (events["event_type"].eq("user") & events["role"].eq("user"))
        )
    )

    events = events.sort_values(
        ["source", "thread_id", "stream_id", "timestamp", "event_index"],
        kind="stable",
        na_position="last",
    ).reset_index(drop=True)
    stream_keys = ["source", "thread_id", "stream_id"]
    events["run_number"] = events.groupby(stream_keys, sort=False)["is_run_start"].cumsum().astype("int64")
    events["run_id"] = pd.NA
    assigned = events["run_number"].gt(0)
    events.loc[assigned, "run_id"] = (
        events.loc[assigned, "source"].astype(str)
        + ":"
        + events.loc[assigned, "thread_id"].astype(str)
        + ":"
        + events.loc[assigned, "stream_id"].astype(str)
        + ":"
        + events.loc[assigned, "run_number"].astype(str)
    )
    return events


def build_model_calls(events: pd.DataFrame) -> pd.DataFrame:
    columns = [
        "source",
        "thread_id",
        "stream_id",
        "export_run_id",
        "run_id",
        "timestamp",
        "model",
        "served_input_tokens",
        "uncached_input_tokens",
        "cached_input_tokens",
        "cache_creation_input_tokens",
        "output_tokens",
        "reasoning_output_tokens",
        "reasoning_tokens_available",
        "non_reasoning_output_tokens",
        "reasoning_share_of_output",
    ]
    call_frames = []

    claude = events[events["source"].eq("claude")].copy()
    claude_usage = claude[
        claude[["input_tokens", "output_tokens", "cached_input_tokens", "cache_creation_input_tokens"]]
        .notna()
        .any(axis=1)
    ].copy()
    claude_usage["uncached_input_tokens"] = claude_usage["input_tokens"].fillna(0)
    claude_usage["cached_input_tokens"] = claude_usage["cached_input_tokens"].fillna(0)
    claude_usage["cache_creation_input_tokens"] = claude_usage["cache_creation_input_tokens"].fillna(0)
    claude_usage["served_input_tokens"] = (
        claude_usage["uncached_input_tokens"]
        + claude_usage["cached_input_tokens"]
        + claude_usage["cache_creation_input_tokens"]
    )
    claude_usage["output_tokens"] = claude_usage["output_tokens"].fillna(0)
    claude_usage["reasoning_output_tokens"] = np.nan
    claude_usage["reasoning_tokens_available"] = False
    claude_usage["non_reasoning_output_tokens"] = np.nan
    claude_usage["reasoning_share_of_output"] = np.nan
    call_frames.append(claude_usage[columns])

    codex = events[
        events["source"].eq("codex")
        & events[["cumulative_input_tokens", "cumulative_output_tokens"]].notna().any(axis=1)
    ].copy()
    codex = codex.sort_values(["thread_id", "event_index"], kind="stable")
    cumulative_columns = {
        "cumulative_input_tokens": "served_input_tokens",
        "cumulative_output_tokens": "output_tokens",
        "cumulative_cached_input_tokens": "cached_input_tokens",
        "cumulative_reasoning_output_tokens": "reasoning_output_tokens",
    }
    for cumulative, output in cumulative_columns.items():
        current = codex[cumulative].fillna(0)
        delta = codex.groupby("thread_id", sort=False)[cumulative].diff()
        delta = delta.fillna(current)
        codex[output] = delta.where(delta.ge(0), current).fillna(0)

    # Repeated event_msg.token_count snapshots have zero cumulative delta and
    # are not additional model calls.
    codex = codex[
        codex[["served_input_tokens", "output_tokens", "cached_input_tokens"]].gt(0).any(axis=1)
    ].copy()
    codex["cache_creation_input_tokens"] = 0.0
    codex["uncached_input_tokens"] = (
        codex["served_input_tokens"] - codex["cached_input_tokens"]
    ).clip(lower=0)
    codex["reasoning_tokens_available"] = codex["cumulative_reasoning_output_tokens"].notna()
    codex["non_reasoning_output_tokens"] = (
        codex["output_tokens"] - codex["reasoning_output_tokens"]
    ).clip(lower=0)
    codex["reasoning_share_of_output"] = safe_ratio(
        codex["reasoning_output_tokens"], codex["output_tokens"]
    )
    call_frames.append(codex[columns])

    model_calls = pd.concat(call_frames, ignore_index=True)
    model_calls["cache_read_ratio"] = safe_ratio(
        model_calls["cached_input_tokens"], model_calls["served_input_tokens"]
    )
    model_calls["cache_creation_ratio"] = safe_ratio(
        model_calls["cache_creation_input_tokens"], model_calls["served_input_tokens"]
    )
    return model_calls.sort_values("timestamp", kind="stable").reset_index(drop=True)


def build_tool_calls(events: pd.DataFrame) -> pd.DataFrame:
    if "is_tool_call" in events.columns:
        request_mask = events["is_tool_call"].eq(True)
        if "tool_event_stage" in events.columns:
            request_mask &= events["tool_event_stage"].eq("call")
    else:
        request_mask = (
            events["source"].eq("codex")
            & events["event_type"].str.startswith("response_item.", na=False)
            & events["event_type"].str.endswith("_call", na=False)
        ) | (events["source"].eq("claude") & events["event_type"].eq("assistant.tool_use"))
    requests = events[request_mask].copy()

    derived_name = (
        requests["event_type"]
        .str.removeprefix("response_item.")
        .str.removesuffix("_call")
    )
    requests["tool_name"] = requests["tool_name"].fillna(derived_name)

    with_id = requests[requests["tool_call_id"].notna()].drop_duplicates(
        ["source", "thread_id", "stream_id", "run_id", "tool_call_id"], keep="first"
    )
    without_id = requests[requests["tool_call_id"].isna()]
    requests = pd.concat([with_id, without_id], ignore_index=True)

    outcomes = events[events["tool_call_id"].notna() & events["success"].notna()].copy()
    if not outcomes.empty:
        outcomes = (
            outcomes.sort_values("timestamp", kind="stable")
            .groupby(["source", "thread_id", "stream_id", "tool_call_id"], as_index=False)
            .tail(1)[["source", "thread_id", "stream_id", "tool_call_id", "success"]]
            .rename(columns={"success": "call_success"})
        )
        requests = requests.merge(
            outcomes,
            on=["source", "thread_id", "stream_id", "tool_call_id"],
            how="left",
        )
    else:
        requests["call_success"] = np.nan

    return requests[
        [
            "source",
            "thread_id",
            "stream_id",
            "export_run_id",
            "run_id",
            "timestamp",
            "tool_call_id",
            "tool_name",
            "call_success",
        ]
    ].sort_values("timestamp", kind="stable").reset_index(drop=True)


def most_common_non_null(series: pd.Series):
    values = series.dropna()
    return values.mode().iloc[0] if not values.empty else None


def build_runs(events: pd.DataFrame, model_calls: pd.DataFrame, tool_calls: pd.DataFrame) -> pd.DataFrame:
    assigned = events[events["run_id"].notna()].copy()
    run_rows = []
    for run_id, group in assigned.groupby("run_id", sort=False):
        source = group["source"].iloc[0]
        prompt_rows = group[group["is_run_start"]]
        start = prompt_rows["timestamp"].min()

        if source == "codex":
            terminal = group[group["event_type"].isin(["event_msg.task_complete", "event_msg.turn_aborted"])]
            assistant = group[
                group["event_type"].eq("response_item.message") & group["role"].eq("assistant")
            ]
        else:
            terminal = group.iloc[0:0]
            assistant = group[group["event_type"].str.startswith("assistant.", na=False)]

        if not terminal.empty:
            end = terminal["timestamp"].max()
            duration_values = terminal["duration_ms"].dropna()
            duration_ms = duration_values.iloc[-1] if not duration_values.empty else np.nan
            duration_source = "reported_duration_ms" if not duration_values.empty else "event_timestamps"
            status = "aborted" if terminal["event_type"].eq("event_msg.turn_aborted").any() else "completed"
        else:
            text_end = assistant[assistant["event_type"].isin(["assistant.text", "response_item.message"])]
            end = text_end["timestamp"].max() if not text_end.empty else assistant["timestamp"].max()
            if pd.isna(end):
                end = group["timestamp"].max()
            duration_ms = np.nan
            duration_source = "event_timestamps"
            status = "inferred_complete" if not text_end.empty else "incomplete"

        if pd.isna(duration_ms) and pd.notna(start) and pd.notna(end):
            duration_ms = (end - start).total_seconds() * 1000

        ttft = group["time_to_first_token_ms"].dropna()
        run_rows.append(
            {
                "run_id": run_id,
                "source": source,
                "thread_id": group["thread_id"].iloc[0],
                "stream_id": group["stream_id"].iloc[0],
                "export_run_ids": ",".join(
                    sorted(group["export_run_id"].dropna().astype(str).unique())
                ),
                "run_number": int(group["run_number"].iloc[0]),
                "is_sidechain": bool(prompt_rows["is_sidechain"].any()),
                "start_time": start,
                "end_time": end,
                "duration_seconds": duration_ms / 1000 if pd.notna(duration_ms) else np.nan,
                "duration_source": duration_source,
                "time_to_first_token_seconds": ttft.iloc[-1] / 1000 if not ttft.empty else np.nan,
                "status": status,
            }
        )

    runs = pd.DataFrame(run_rows)

    call_totals = model_calls[model_calls["run_id"].notna()].groupby("run_id").agg(
        model_calls=("run_id", "size"),
        served_input_tokens=("served_input_tokens", "sum"),
        uncached_input_tokens=("uncached_input_tokens", "sum"),
        cached_input_tokens=("cached_input_tokens", "sum"),
        cache_creation_input_tokens=("cache_creation_input_tokens", "sum"),
        output_tokens=("output_tokens", "sum"),
        reasoning_output_tokens=("reasoning_output_tokens", lambda values: values.sum(min_count=1)),
        reasoning_token_calls=("reasoning_tokens_available", "sum"),
        model=("model", most_common_non_null),
    )
    tool_totals = tool_calls[tool_calls["run_id"].notna()].groupby("run_id").agg(
        tool_calls=("run_id", "size"),
        known_tool_outcomes=("call_success", "count"),
        failed_tool_calls=("call_success", lambda values: values.eq(False).sum()),
    )
    runs = runs.merge(call_totals, on="run_id", how="left").merge(tool_totals, on="run_id", how="left")

    count_columns = [
        "model_calls",
        "reasoning_token_calls",
        "tool_calls",
        "known_tool_outcomes",
        "failed_tool_calls",
    ]
    token_columns = [
        "served_input_tokens",
        "uncached_input_tokens",
        "cached_input_tokens",
        "cache_creation_input_tokens",
        "output_tokens",
    ]
    runs[count_columns] = runs[count_columns].fillna(0).astype(int)
    runs[token_columns] = runs[token_columns].fillna(0)

    # Imported/resumed histories can contain an entire prior run whose events
    # were all stamped within a millisecond. Preserve that raw observation for
    # auditing, but do not treat it as a real duration measurement.
    runs["raw_duration_seconds"] = runs["duration_seconds"]
    has_activity = runs[["model_calls", "tool_calls", "output_tokens"]].gt(0).any(axis=1)
    collapsed = (
        runs["duration_source"].eq("event_timestamps")
        & runs["duration_seconds"].le(0.1)
        & has_activity
    )
    runs["duration_quality"] = np.where(collapsed, "collapsed_timestamps", "usable")
    runs.loc[collapsed, "duration_seconds"] = np.nan
    runs["cache_read_ratio"] = safe_ratio(runs["cached_input_tokens"], runs["served_input_tokens"])
    runs["cache_creation_ratio"] = safe_ratio(
        runs["cache_creation_input_tokens"], runs["served_input_tokens"]
    )
    runs["reasoning_tokens_available"] = runs["reasoning_token_calls"].gt(0)
    runs["non_reasoning_output_tokens"] = (
        runs["output_tokens"] - runs["reasoning_output_tokens"]
    ).clip(lower=0)
    runs["reasoning_share_of_output"] = safe_ratio(
        runs["reasoning_output_tokens"], runs["output_tokens"]
    )
    runs["tool_failure_ratio"] = safe_ratio(runs["failed_tool_calls"], runs["known_tool_outcomes"])
    runs["end_to_end_output_tokens_per_second"] = safe_ratio(
        runs["output_tokens"], runs["duration_seconds"]
    )
    return runs.sort_values("start_time", kind="stable").reset_index(drop=True)


def build_threads(
    events: pd.DataFrame,
    runs: pd.DataFrame,
    model_calls: pd.DataFrame,
    tool_calls: pd.DataFrame,
) -> pd.DataFrame:
    base = events.groupby(["source", "thread_id"], as_index=False).agg(
        thread_start=("timestamp", "min"),
        thread_end=("timestamp", "max"),
        events=("thread_id", "size"),
        is_sidechain=("is_sidechain", "max"),
        user_messages=("is_user_prompt", "sum"),
        top_level_user_messages=("is_top_level_user_prompt", "sum"),
    )
    base["thread_span_hours"] = (base["thread_end"] - base["thread_start"]).dt.total_seconds() / 3600

    run_totals = runs.groupby(["source", "thread_id"], as_index=False).agg(
        runs=("run_id", "size"),
        active_duration_seconds=("duration_seconds", "sum"),
        median_run_duration_seconds=("duration_seconds", "median"),
    )
    call_totals = model_calls.groupby(["source", "thread_id"], as_index=False).agg(
        model_calls=("thread_id", "size"),
        served_input_tokens=("served_input_tokens", "sum"),
        uncached_input_tokens=("uncached_input_tokens", "sum"),
        cached_input_tokens=("cached_input_tokens", "sum"),
        cache_creation_input_tokens=("cache_creation_input_tokens", "sum"),
        output_tokens=("output_tokens", "sum"),
        reasoning_output_tokens=("reasoning_output_tokens", lambda values: values.sum(min_count=1)),
        reasoning_token_calls=("reasoning_tokens_available", "sum"),
    )
    tool_totals = tool_calls.groupby(["source", "thread_id"], as_index=False).agg(
        tool_calls=("thread_id", "size"),
        known_tool_outcomes=("call_success", "count"),
        failed_tool_calls=("call_success", lambda values: values.eq(False).sum()),
    )

    threads = base.merge(run_totals, on=["source", "thread_id"], how="left")
    threads = threads.merge(call_totals, on=["source", "thread_id"], how="left")
    threads = threads.merge(tool_totals, on=["source", "thread_id"], how="left")
    count_columns = [
        "user_messages",
        "runs",
        "model_calls",
        "reasoning_token_calls",
        "tool_calls",
        "known_tool_outcomes",
        "failed_tool_calls",
    ]
    token_columns = [
        "served_input_tokens",
        "uncached_input_tokens",
        "cached_input_tokens",
        "cache_creation_input_tokens",
        "output_tokens",
    ]
    threads[count_columns] = threads[count_columns].fillna(0).astype(int)
    threads[token_columns] = threads[token_columns].fillna(0)
    threads["cache_read_ratio"] = safe_ratio(threads["cached_input_tokens"], threads["served_input_tokens"])
    threads["cache_creation_ratio"] = safe_ratio(
        threads["cache_creation_input_tokens"], threads["served_input_tokens"]
    )
    threads["reasoning_tokens_available"] = threads["reasoning_token_calls"].gt(0)
    threads["non_reasoning_output_tokens"] = (
        threads["output_tokens"] - threads["reasoning_output_tokens"]
    ).clip(lower=0)
    threads["reasoning_share_of_output"] = safe_ratio(
        threads["reasoning_output_tokens"], threads["output_tokens"]
    )
    threads["tool_failure_ratio"] = safe_ratio(
        threads["failed_tool_calls"], threads["known_tool_outcomes"]
    )
    return threads.sort_values("thread_start", kind="stable").reset_index(drop=True)


def summarize(events: pd.DataFrame, runs: pd.DataFrame, threads: pd.DataFrame) -> pd.DataFrame:
    rows = []
    for scope in ["all", *sorted(events["source"].dropna().unique())]:
        scope_events = events if scope == "all" else events[events["source"].eq(scope)]
        scope_runs = runs if scope == "all" else runs[runs["source"].eq(scope)]
        scope_threads = threads if scope == "all" else threads[threads["source"].eq(scope)]
        served = scope_runs["served_input_tokens"].sum()
        known_outcomes = scope_runs["known_tool_outcomes"].sum()
        reasoning_runs = scope_runs[scope_runs["reasoning_tokens_available"]]
        reasoning_threads = scope_threads[scope_threads["reasoning_tokens_available"]]
        rows.append(
            {
                "scope": scope,
                "events": len(scope_events),
                "threads": len(scope_threads),
                "runs": len(scope_runs),
                "top_level_runs": (~scope_runs["is_sidechain"]).sum(),
                "sidechain_runs": scope_runs["is_sidechain"].sum(),
                "avg_duration_seconds_per_run": scope_runs["duration_seconds"].mean(),
                "median_duration_seconds_per_run": scope_runs["duration_seconds"].median(),
                "p90_duration_seconds_per_run": percentile(scope_runs["duration_seconds"], 0.90),
                "p95_duration_seconds_per_run": percentile(scope_runs["duration_seconds"], 0.95),
                "p99_duration_seconds_per_run": percentile(scope_runs["duration_seconds"], 0.99),
                "avg_tool_calls_per_run": scope_runs["tool_calls"].mean(),
                "median_tool_calls_per_run": scope_runs["tool_calls"].median(),
                "p95_tool_calls_per_run": percentile(scope_runs["tool_calls"], 0.95),
                "avg_user_messages_per_thread": scope_threads["user_messages"].mean(),
                "avg_top_level_user_messages_per_thread": scope_threads["top_level_user_messages"].mean(),
                "avg_model_calls_per_run": scope_runs["model_calls"].mean(),
                "avg_served_input_tokens_per_run": scope_runs["served_input_tokens"].mean(),
                "avg_output_tokens_per_run": scope_runs["output_tokens"].mean(),
                "avg_served_input_tokens_per_thread": scope_threads["served_input_tokens"].mean(),
                "avg_output_tokens_per_thread": scope_threads["output_tokens"].mean(),
                "runs_with_reasoning_token_data": len(reasoning_runs),
                "avg_reasoning_output_tokens_per_run": reasoning_runs[
                    "reasoning_output_tokens"
                ].mean(),
                "avg_reasoning_output_tokens_per_thread": reasoning_threads[
                    "reasoning_output_tokens"
                ].mean(),
                "aggregate_reasoning_share_of_output": safe_ratio(
                    reasoning_runs["reasoning_output_tokens"].sum(),
                    reasoning_runs["output_tokens"].sum(),
                ),
                "aggregate_cache_read_ratio": safe_ratio(scope_runs["cached_input_tokens"].sum(), served),
                "aggregate_cache_creation_ratio": safe_ratio(
                    scope_runs["cache_creation_input_tokens"].sum(), served
                ),
                "avg_thread_cache_read_ratio": scope_threads["cache_read_ratio"].mean(),
                "avg_time_to_first_token_seconds": scope_runs["time_to_first_token_seconds"].mean(),
                "p95_time_to_first_token_seconds": percentile(
                    scope_runs["time_to_first_token_seconds"], 0.95
                ),
                "aborted_run_ratio": scope_runs["status"].eq("aborted").mean(),
                "known_tool_failure_ratio": safe_ratio(
                    scope_runs["failed_tool_calls"].sum(), known_outcomes
                ),
                "aggregate_end_to_end_output_tokens_per_second": safe_ratio(
                    scope_runs.loc[scope_runs["duration_seconds"].notna(), "output_tokens"].sum(),
                    scope_runs["duration_seconds"].sum(),
                ),
            }
        )
    return pd.DataFrame(rows)


def distribution_summary(runs: pd.DataFrame, threads: pd.DataFrame) -> pd.DataFrame:
    datasets = {
        "run": (
            runs,
            [
                "duration_seconds",
                "tool_calls",
                "model_calls",
                "served_input_tokens",
                "output_tokens",
                "reasoning_output_tokens",
                "non_reasoning_output_tokens",
                "reasoning_share_of_output",
                "cache_read_ratio",
                "cache_creation_ratio",
                "time_to_first_token_seconds",
            ],
        ),
        "thread": (
            threads,
            [
                "user_messages",
                "active_duration_seconds",
                "tool_calls",
                "model_calls",
                "served_input_tokens",
                "output_tokens",
                "reasoning_output_tokens",
                "non_reasoning_output_tokens",
                "reasoning_share_of_output",
                "cache_read_ratio",
                "cache_creation_ratio",
            ],
        ),
    }
    rows = []
    for level, (frame, metrics) in datasets.items():
        for scope in ["all", *sorted(frame["source"].unique())]:
            scoped = frame if scope == "all" else frame[frame["source"].eq(scope)]
            for metric in metrics:
                values = scoped[metric].replace([np.inf, -np.inf], np.nan).dropna()
                rows.append(
                    {
                        "level": level,
                        "scope": scope,
                        "metric": metric,
                        "count": len(values),
                        "mean": values.mean(),
                        "std": values.std(),
                        "min": values.min(),
                        "p50": values.median(),
                        "p90": values.quantile(0.90),
                        "p95": values.quantile(0.95),
                        "p99": values.quantile(0.99),
                        "max": values.max(),
                    }
                )
    return pd.DataFrame(rows)


def plot_ecdf(frame: pd.DataFrame, column: str, title: str, xlabel: str, path: Path, log_x=False):
    fig, ax = plt.subplots(figsize=(8, 5))
    groups = [("all", frame), *list(frame.groupby("source"))]
    for source, group in groups:
        values = group[column].replace([np.inf, -np.inf], np.nan).dropna()
        if log_x:
            values = values[values.gt(0)]
        values = np.sort(values.to_numpy())
        if len(values):
            style = {"color": "black", "linestyle": "--", "linewidth": 1.5} if source == "all" else {}
            ax.plot(values, np.arange(1, len(values) + 1) / len(values), label=source, **style)
    if log_x:
        ax.set_xscale("log")
    ax.set_title(title)
    ax.set_xlabel(xlabel)
    ax.set_ylabel("Fraction of observations ≤ x")
    ax.grid(alpha=0.25)
    ax.legend()
    fig.tight_layout()
    fig.savefig(path, dpi=160)
    plt.close(fig)


def make_plots(
    runs: pd.DataFrame,
    threads: pd.DataFrame,
    model_calls: pd.DataFrame,
    tool_calls: pd.DataFrame,
    output_dir: Path,
) -> None:
    plot_ecdf(
        runs,
        "duration_seconds",
        "Agent run duration",
        "Duration (seconds, log scale)",
        output_dir / "run_duration_ecdf.png",
        log_x=True,
    )
    plot_ecdf(
        runs,
        "tool_calls",
        "Tool calls per run",
        "Tool calls",
        output_dir / "tool_calls_per_run_ecdf.png",
    )
    plot_ecdf(
        threads,
        "user_messages",
        "User-role prompts per thread",
        "User-role prompts",
        output_dir / "user_messages_per_thread_ecdf.png",
    )

    fig, axes = plt.subplots(1, 2, figsize=(12, 5))
    for source, group in runs.groupby("source"):
        values = group["cache_read_ratio"].dropna()
        axes[0].hist(values, bins=np.linspace(0, 1, 31), alpha=0.55, label=source)
    axes[0].set(title="Run cache-read ratio", xlabel="Cached / served input", ylabel="Runs")
    axes[0].legend()
    axes[0].grid(alpha=0.2)

    for source, group in threads.groupby("source"):
        values = group["cache_read_ratio"].dropna()
        axes[1].hist(values, bins=np.linspace(0, 1, 31), alpha=0.55, label=source)
    axes[1].set(title="Thread cache-read ratio", xlabel="Cached / served input", ylabel="Threads")
    axes[1].legend()
    axes[1].grid(alpha=0.2)
    fig.tight_layout()
    fig.savefig(output_dir / "cache_ratios.png", dpi=160)
    plt.close(fig)

    fig, axes = plt.subplots(1, 2, figsize=(12, 5))
    rng = np.random.default_rng(7)
    sample_index = rng.choice(len(runs), size=min(len(runs), 5000), replace=False) if len(runs) else []
    sample = runs.iloc[sample_index] if len(runs) else runs
    for source, group in sample.groupby("source"):
        valid = group[group["served_input_tokens"].gt(0) & group["duration_seconds"].gt(0)]
        axes[0].scatter(
            valid["served_input_tokens"], valid["duration_seconds"], s=12, alpha=0.35, label=source
        )
        valid = group[group["tool_calls"].gt(0) & group["duration_seconds"].gt(0)]
        axes[1].scatter(valid["tool_calls"], valid["duration_seconds"], s=12, alpha=0.35, label=source)
    axes[0].set(
        title="Input load vs. end-to-end duration",
        xlabel="Served input tokens (log)",
        ylabel="Duration seconds (log)",
        xscale="log",
        yscale="log",
    )
    axes[1].set(
        title="Tool calls vs. end-to-end duration",
        xlabel="Tool calls (log)",
        ylabel="Duration seconds (log)",
        xscale="log",
        yscale="log",
    )
    for ax in axes:
        ax.grid(alpha=0.2)
        ax.legend()
    fig.tight_layout()
    fig.savefig(output_dir / "run_load_vs_duration.png", dpi=160)
    plt.close(fig)

    token_plot = runs.melt(
        id_vars="source",
        value_vars=["served_input_tokens", "output_tokens"],
        var_name="token_type",
        value_name="tokens",
    )
    token_plot = token_plot[token_plot["tokens"].gt(0)].copy()
    labels = []
    values = []
    for (source, token_type), group in token_plot.groupby(["source", "token_type"], sort=True):
        labels.append(f"{source}\n{token_type.replace('_tokens', '').replace('_', ' ')}")
        values.append(group["tokens"].to_numpy())
    if values:
        fig, ax = plt.subplots(figsize=(9, 5))
        ax.boxplot(values, tick_labels=labels, showfliers=False)
        ax.set_yscale("log")
        ax.set(title="Tokens per run", ylabel="Tokens (log scale)")
        ax.grid(axis="y", alpha=0.25)
        fig.tight_layout()
        fig.savefig(output_dir / "tokens_per_run.png", dpi=160)
        plt.close(fig)

    reasoning_calls = model_calls[
        model_calls["reasoning_tokens_available"] & model_calls["output_tokens"].gt(0)
    ].copy()
    if not reasoning_calls.empty:
        fig, axes = plt.subplots(1, 2, figsize=(13, 5))
        positive = reasoning_calls[reasoning_calls["reasoning_output_tokens"].gt(0)]
        if not positive.empty:
            density = axes[0].hexbin(
                positive["output_tokens"],
                positive["reasoning_output_tokens"],
                xscale="log",
                yscale="log",
                gridsize=40,
                bins="log",
                mincnt=1,
                cmap="viridis",
            )
            lower = min(positive["output_tokens"].min(), positive["reasoning_output_tokens"].min())
            upper = max(positive["output_tokens"].max(), positive["reasoning_output_tokens"].max())
            axes[0].plot([lower, upper], [lower, upper], color="black", linestyle="--", linewidth=1)
            fig.colorbar(density, ax=axes[0], label="Model-call count (log color scale)")
        axes[0].set(
            title="Reasoning tokens vs. total output per model call",
            xlabel="Total output tokens (log)",
            ylabel="Reasoning output tokens (log)",
        )
        axes[0].grid(alpha=0.2)

        axes[1].hist(
            reasoning_calls["reasoning_share_of_output"].dropna(),
            bins=np.linspace(0, 1, 31),
            alpha=0.6,
            density=True,
            label="model calls",
        )
        reasoning_runs = runs[
            runs["reasoning_tokens_available"] & runs["reasoning_share_of_output"].notna()
        ]
        axes[1].hist(
            reasoning_runs["reasoning_share_of_output"],
            bins=np.linspace(0, 1, 31),
            alpha=0.6,
            density=True,
            label="runs",
        )
        axes[1].set(
            title="Reasoning share of total output",
            xlabel="Reasoning tokens / total output tokens",
            ylabel="Density",
        )
        axes[1].grid(alpha=0.2)
        axes[1].legend()
        fig.tight_layout()
        fig.savefig(output_dir / "reasoning_vs_output_tokens.png", dpi=160)
        plt.close(fig)

    source_names = sorted(tool_calls["source"].dropna().unique())
    if source_names:
        fig, axes = plt.subplots(1, len(source_names), figsize=(7 * len(source_names), 6), squeeze=False)
        for ax, source in zip(axes[0], source_names):
            top_tools = tool_calls[tool_calls["source"].eq(source)]["tool_name"].value_counts().head(12)
            top_tools.sort_values().plot.barh(ax=ax)
            ax.set(title=f"Most-used tools: {source}", xlabel="Calls", ylabel="Tool")
            ax.grid(axis="x", alpha=0.25)
        fig.tight_layout()
        fig.savefig(output_dir / "top_tools.png", dpi=160)
        plt.close(fig)

    # These were produced by earlier versions but are not useful for a
    # single-user trace. Remove stale copies when reusing an output directory.
    (output_dir / "daily_load.csv").unlink(missing_ok=True)
    (output_dir / "daily_load.png").unlink(missing_ok=True)


def main() -> None:
    parser = argparse.ArgumentParser(description="Analyze Codex and Claude coding-agent event threads.")
    parser.add_argument(
        "--input",
        type=Path,
        default=Path(__file__).parent / "sanitized" / "threads.parquet",
        help="Input event parquet",
    )
    parser.add_argument(
        "--output-dir",
        type=Path,
        default=Path(__file__).parent / "output",
        help="Directory for CSV tables and PNG plots",
    )
    args = parser.parse_args()
    args.output_dir.mkdir(parents=True, exist_ok=True)

    events = load_events(args.input)
    model_calls = build_model_calls(events)
    tool_calls = build_tool_calls(events)
    runs = build_runs(events, model_calls, tool_calls)
    threads = build_threads(events, runs, model_calls, tool_calls)
    summary = summarize(events, runs, threads)
    distributions = distribution_summary(runs, threads)

    summary.to_csv(args.output_dir / "summary.csv", index=False)
    distributions.to_csv(args.output_dir / "distribution_summary.csv", index=False)
    runs.to_csv(args.output_dir / "runs.csv", index=False)
    threads.to_csv(args.output_dir / "threads.csv", index=False)
    model_calls.to_csv(args.output_dir / "model_calls.csv", index=False)
    tool_calls.to_csv(args.output_dir / "tool_calls.csv", index=False)

    model_usage = model_calls.copy()
    model_usage["model"] = model_usage["model"].fillna("unknown")
    model_usage = model_usage.groupby(["source", "model"], as_index=False).agg(
        calls=("model", "size"),
        served_input_tokens=("served_input_tokens", "sum"),
        cached_input_tokens=("cached_input_tokens", "sum"),
        cache_creation_input_tokens=("cache_creation_input_tokens", "sum"),
        output_tokens=("output_tokens", "sum"),
        reasoning_output_tokens=("reasoning_output_tokens", lambda values: values.sum(min_count=1)),
        reasoning_token_calls=("reasoning_tokens_available", "sum"),
        avg_served_input_tokens=("served_input_tokens", "mean"),
        p50_served_input_tokens=("served_input_tokens", "median"),
        p95_served_input_tokens=("served_input_tokens", lambda values: values.quantile(0.95)),
        avg_output_tokens=("output_tokens", "mean"),
        avg_reasoning_output_tokens=("reasoning_output_tokens", "mean"),
        p95_reasoning_output_tokens=("reasoning_output_tokens", lambda values: values.quantile(0.95)),
    )
    model_usage["cache_read_ratio"] = safe_ratio(
        model_usage["cached_input_tokens"], model_usage["served_input_tokens"]
    )
    model_usage["cache_creation_ratio"] = safe_ratio(
        model_usage["cache_creation_input_tokens"], model_usage["served_input_tokens"]
    )
    model_usage["reasoning_tokens_available"] = model_usage["reasoning_token_calls"].gt(0)
    model_usage["reasoning_share_of_output"] = safe_ratio(
        model_usage["reasoning_output_tokens"], model_usage["output_tokens"]
    )
    model_usage.sort_values(["source", "calls"], ascending=[True, False]).to_csv(
        args.output_dir / "model_usage.csv", index=False
    )

    tool_usage = tool_calls.groupby(["source", "tool_name"], as_index=False).agg(
        calls=("tool_name", "size"),
        known_outcomes=("call_success", "count"),
        failures=("call_success", lambda values: values.eq(False).sum()),
    )
    tool_usage["failure_ratio"] = safe_ratio(tool_usage["failures"], tool_usage["known_outcomes"])
    tool_usage.sort_values(["source", "calls"], ascending=[True, False]).to_csv(
        args.output_dir / "tool_usage.csv", index=False
    )

    data_quality = pd.DataFrame(
        [
            {"check": "events", "value": len(events)},
            {"check": "invalid_timestamps", "value": events["timestamp"].isna().sum()},
            {
                "check": "threads_without_canonical_user_prompt",
                "value": (threads["user_messages"] == 0).sum(),
            },
            {"check": "model_calls_before_first_prompt", "value": model_calls["run_id"].isna().sum()},
            {"check": "tool_calls_before_first_prompt", "value": tool_calls["run_id"].isna().sum()},
            {
                "check": "runs_with_collapsed_timestamps",
                "value": runs["duration_quality"].eq("collapsed_timestamps").sum(),
            },
            {"check": "runs_without_usable_duration", "value": runs["duration_seconds"].isna().sum()},
            {
                "check": "duplicate_thread_event_indexes",
                "value": events.duplicated(["source", "thread_id", "stream_id", "event_index"]).sum(),
            },
        ]
    )
    data_quality.to_csv(args.output_dir / "data_quality.csv", index=False)

    make_plots(runs, threads, model_calls, tool_calls, args.output_dir)

    with pd.option_context("display.max_columns", None, "display.width", 220):
        print(summary.round(3).to_string(index=False))
    print(f"\nWrote analysis to {args.output_dir.resolve()}")


if __name__ == "__main__":
    main()
