import argparse
import html
import json
import os
import subprocess
import sys
import webbrowser
from datetime import datetime
from pathlib import Path
from urllib.parse import quote

import pandas as pd


SCRIPT_DIR = Path(__file__).resolve().parent

CHARTS = {
    "run_duration_hist.png": ("Run duration", "Distribution of end-to-end run duration."),
    "run_duration_ecdf.png": ("Run duration percentiles", "Cumulative view of run duration."),
    "response_gap_hist.png": ("Response gaps", "Idle time between an answer and the next prompt."),
    "response_gap_ecdf.png": ("Response gap percentiles", "Cumulative view of user think time."),
    "tool_calls_per_run_hist.png": ("Tool calls per run", "How tool-heavy individual runs are."),
    "tool_calls_per_run_ecdf.png": ("Tool-call percentiles", "Cumulative view of tool calls per run."),
    "user_messages_per_thread_hist.png": ("Messages per thread", "Conversation length by user messages."),
    "user_messages_per_thread_ecdf.png": ("Thread-length percentiles", "Cumulative conversation length."),
    "cache_ratios.png": ("Cache usage", "Cache-read ratios at run and thread level."),
    "run_load_vs_duration.png": ("Load and duration", "Input size and tool calls compared with duration."),
    "tokens_per_run.png": ("Tokens per run", "Input and output token distributions."),
    "reasoning_vs_output_tokens.png": ("Reasoning tokens", "Reasoning volume and share of model output."),
    "top_tools.png": ("Most-used tools", "The most frequent tools for each source."),
}

BASE_CSS = """
:root { color-scheme: light; --ink: #17202a; --muted: #667085; --line: #e4e7ec;
  --paper: #fff; --wash: #f4f6f8; --accent: #3157d5; }
* { box-sizing: border-box; }
body { margin: 0; color: var(--ink); background: var(--wash); font: 15px/1.55 system-ui, sans-serif; }
main { width: min(1440px, calc(100% - 32px)); margin: 0 auto; padding: 42px 0 72px; }
h1, h2, h3 { line-height: 1.2; margin-top: 0; }
h1 { font-size: clamp(30px, 5vw, 52px); letter-spacing: -.04em; margin-bottom: 10px; }
h2 { margin: 42px 0 16px; font-size: 25px; }
p { margin: 0; } .muted { color: var(--muted); }
.eyebrow { color: var(--accent); font-weight: 700; letter-spacing: .08em; text-transform: uppercase; }
.panel, .card { background: var(--paper); border: 1px solid var(--line); border-radius: 14px; }
.panel { padding: 20px; overflow: auto; }
.cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 12px; margin-top: 26px; }
.card { padding: 18px; } .card strong { display: block; font-size: 28px; }
.charts { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(460px, 100%), 1fr)); gap: 16px; }
.chart { padding: 14px; } .chart img { display: block; width: 100%; border-radius: 8px; }
.chart h3 { margin: 12px 4px 3px; } .chart p { margin: 0 4px 4px; color: var(--muted); }
table { width: 100%; border-collapse: collapse; white-space: nowrap; }
th, td { padding: 9px 12px; border-bottom: 1px solid var(--line); text-align: left; }
th { color: var(--muted); font-size: 12px; text-transform: uppercase; letter-spacing: .04em; }
tbody tr:hover { background: #f8f9fc; }
a { color: var(--accent); text-decoration: none; } a:hover { text-decoration: underline; }
input[type=search] { width: min(480px, 100%); padding: 11px 13px; margin-bottom: 14px;
  border: 1px solid #cfd4dc; border-radius: 9px; background: white; font: inherit; }
.transcript { max-width: 920px; margin: 0 auto; }
.message { margin: 14px 0; padding: 16px 18px; border: 1px solid var(--line); border-radius: 12px; background: white; }
.message.user { border-left: 4px solid #3157d5; } .message.assistant { border-left: 4px solid #15956a; }
.message.tool { border-left: 4px solid #b47700; background: #fffcf4; }
.role { font-size: 12px; font-weight: 750; letter-spacing: .06em; text-transform: uppercase; }
.content { margin-top: 8px; white-space: pre-wrap; overflow-wrap: anywhere; }
details pre { overflow: auto; white-space: pre-wrap; font-size: 12px; }
@media (max-width: 640px) { main { width: min(100% - 20px, 1440px); padding-top: 24px; } }
"""


def read_jsonl(path: Path) -> list[dict]:
    records = []
    with path.open(encoding="utf-8", errors="replace") as file:
        for line in file:
            try:
                value = json.loads(line)
            except json.JSONDecodeError:
                continue
            if isinstance(value, dict):
                records.append(value)
    return records


def content_text(value: object) -> str:
    if isinstance(value, str):
        return value
    if isinstance(value, list):
        parts = []
        for item in value:
            if isinstance(item, str):
                parts.append(item)
            elif isinstance(item, dict):
                text = item.get("text") or item.get("input_text") or item.get("output_text")
                if text:
                    parts.append(str(text))
        return "\n".join(parts)
    if value is None:
        return ""
    return str(value)


def json_text(value: object, limit: int = 30_000) -> str:
    if isinstance(value, str):
        text = value
    else:
        text = json.dumps(value, ensure_ascii=False, indent=2)
    if len(text) > limit:
        return text[:limit] + f"\n\n… truncated {len(text) - limit:,} characters"
    return text


def codex_messages(records: list[dict]) -> list[tuple[str, str, str | None]]:
    messages = []
    tool_names = {}
    for record in records:
        record_type = record.get("type")
        payload = record.get("payload") or {}
        payload_type = payload.get("type")
        call_id = payload.get("call_id")

        if record_type == "event_msg" and payload_type == "user_message":
            text = content_text(payload.get("message"))
            if text:
                messages.append(("user", text, None))
        elif record_type == "response_item" and payload_type == "message" and payload.get("role") == "assistant":
            text = content_text(payload.get("content"))
            if text:
                messages.append(("assistant", text, None))
        elif record_type == "response_item" and payload_type in {"function_call", "custom_tool_call"}:
            name = payload.get("name") or "tool"
            if call_id:
                tool_names[call_id] = name
            value = payload.get("arguments") if payload_type == "function_call" else payload.get("input")
            messages.append(("tool", f"Call: {name}", json_text(value)))
        elif record_type == "response_item" and payload_type in {"function_call_output", "custom_tool_call_output"}:
            name = tool_names.get(call_id, "tool")
            messages.append(("tool", f"Result: {name}", json_text(payload.get("output"))))
    return messages


def claude_messages(records: list[dict]) -> list[tuple[str, str, str | None]]:
    messages = []
    tool_names = {}
    for record in records:
        record_type = record.get("type")
        if record_type not in {"user", "assistant"}:
            continue
        content = (record.get("message") or {}).get("content")
        blocks = content if isinstance(content, list) else [content]
        for block in blocks:
            if isinstance(block, str):
                if block:
                    messages.append((record_type, block, None))
                continue
            if not isinstance(block, dict):
                continue
            block_type = block.get("type")
            if block_type == "text" and block.get("text"):
                messages.append((record_type, str(block["text"]), None))
            elif block_type == "tool_use":
                name = block.get("name") or "tool"
                if block.get("id"):
                    tool_names[block["id"]] = name
                messages.append(("tool", f"Call: {name}", json_text(block.get("input"))))
            elif block_type == "tool_result":
                name = tool_names.get(block.get("tool_use_id"), "tool")
                messages.append(("tool", f"Result: {name}", json_text(block.get("content"))))
    return messages


def render_messages(messages: list[tuple[str, str, str | None]]) -> str:
    rendered = []
    for role, text, details in messages:
        details_html = ""
        if details:
            details_html = f"<details><summary>Show details</summary><pre>{html.escape(details)}</pre></details>"
        rendered.append(
            f'<article class="message {role}"><div class="role">{html.escape(role)}</div>'
            f'<div class="content">{html.escape(text)}</div>{details_html}</article>'
        )
    return "\n".join(rendered) or '<div class="panel muted">No readable messages were found.</div>'


def write_transcript(source: str, path: Path, destination: Path) -> tuple[int, str | None, str | None]:
    records = read_jsonl(path)
    messages = codex_messages(records) if source == "codex" else claude_messages(records)
    timestamps = [str(record["timestamp"]) for record in records if record.get("timestamp")]
    first_timestamp = min(timestamps) if timestamps else None
    last_timestamp = max(timestamps) if timestamps else None
    body = render_messages(messages)
    title = f"{source.title()} transcript · {path.name}"
    document = f"""<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{html.escape(title)}</title><style>{BASE_CSS}</style></head>
<body><main class="transcript"><a href="../index.html">← Back to report</a>
<p class="eyebrow" style="margin-top:30px">{html.escape(source)}</p><h1>{html.escape(path.name)}</h1>
<p class="muted">{len(messages):,} entries · rendered locally from UTF-8 JSONL</p>{body}</main></body></html>"""
    destination.write_text(document, encoding="utf-8", newline="\n")
    return len(messages), first_timestamp, last_timestamp


def discover_jsonl(path: Path) -> list[Path]:
    if path.is_file():
        return [path]
    if not path.exists():
        return []
    return sorted(path.rglob("*.jsonl"))


def format_value(value: object) -> str:
    if pd.isna(value):
        return "—"
    if isinstance(value, float):
        if abs(value) >= 1000:
            return f"{value:,.0f}"
        return f"{value:,.3f}".rstrip("0").rstrip(".")
    if isinstance(value, int):
        return f"{value:,}"
    return str(value)


def render_table(frame: pd.DataFrame, columns: list[str] | None = None) -> str:
    if columns is not None:
        columns = [column for column in columns if column in frame.columns]
        frame = frame[columns]
    headers = "".join(f"<th>{html.escape(column.replace('_', ' '))}</th>" for column in frame.columns)
    rows = []
    for values in frame.itertuples(index=False, name=None):
        cells = "".join(f"<td>{html.escape(format_value(value))}</td>" for value in values)
        rows.append(f"<tr>{cells}</tr>")
    return f"<table><thead><tr>{headers}</tr></thead><tbody>{''.join(rows)}</tbody></table>"


def run_pipeline(codex_input: Path, claude_input: Path, output_dir: Path) -> None:
    data_dir = output_dir / "data"
    data_dir.mkdir(parents=True, exist_ok=True)
    for filename in CHARTS:
        (data_dir / filename).unlink(missing_ok=True)
    env = os.environ.copy()
    env["MPLBACKEND"] = "Agg"
    subprocess.run(
        [
            sys.executable,
            str(SCRIPT_DIR / "main.py"),
            "--codex-input", str(codex_input),
            "--claude-input", str(claude_input),
            "--output", str(data_dir / "threads.parquet"),
            "--runs-output", str(data_dir / "source_runs.parquet"),
        ],
        cwd=SCRIPT_DIR,
        env=env,
        check=True,
    )
    subprocess.run(
        [
            sys.executable,
            str(SCRIPT_DIR / "analyze_threads.py"),
            "--input", str(data_dir / "threads.parquet"),
            "--output-dir", str(data_dir),
        ],
        cwd=SCRIPT_DIR,
        env=env,
        check=True,
    )


def build_report(output_dir: Path, sources: list[tuple[str, list[Path]]], title: str) -> Path:
    data_dir = output_dir / "data"
    transcript_dir = output_dir / "transcripts"
    transcript_dir.mkdir(parents=True, exist_ok=True)
    for old_transcript in transcript_dir.glob("*.html"):
        old_transcript.unlink()

    transcript_rows = []
    index = 0
    for source, paths in sources:
        for path in paths:
            index += 1
            filename = f"{index:05d}-{source}.html"
            message_count, first_timestamp, last_timestamp = write_transcript(
                source, path, transcript_dir / filename
            )
            transcript_rows.append(
                {
                    "source": source,
                    "file": path.name,
                    "messages": message_count,
                    "first_timestamp": first_timestamp or "—",
                    "last_timestamp": last_timestamp or "—",
                    "href": f"transcripts/{quote(filename)}",
                }
            )

    summary = pd.read_csv(data_dir / "summary.csv")
    all_summary = summary[summary["scope"].eq("all")].iloc[0]
    cards = [
        ("Threads", all_summary.get("threads")),
        ("Runs", all_summary.get("runs")),
        ("Events", all_summary.get("events")),
        ("Median run", f"{format_value(all_summary.get('median_duration_seconds_per_run'))} s"),
        ("Tool calls / run", format_value(all_summary.get("avg_tool_calls_per_run"))),
        ("Transcripts", len(transcript_rows)),
    ]
    cards_html = "".join(
        f'<div class="card"><span class="muted">{html.escape(label)}</span><strong>{html.escape(format_value(value))}</strong></div>'
        for label, value in cards
    )

    chart_html = []
    for filename, (heading, description) in CHARTS.items():
        if (data_dir / filename).exists():
            url = f"data/{quote(filename)}"
            chart_html.append(
                f'<article class="panel chart"><a href="{url}"><img src="{url}" alt="{html.escape(heading)}"></a>'
                f'<h3>{html.escape(heading)}</h3><p>{html.escape(description)}</p></article>'
            )

    transcript_html = []
    for row in transcript_rows:
        searchable = html.escape(f"{row['source']} {row['file']}", quote=True)
        transcript_html.append(
            f'<tr data-search="{searchable.lower()}"><td>{html.escape(row["source"])}</td>'
            f'<td><a href="{row["href"]}">{html.escape(row["file"])}</a></td>'
            f'<td>{row["messages"]:,}</td><td>{html.escape(row["first_timestamp"])}</td>'
            f'<td>{html.escape(row["last_timestamp"])}</td></tr>'
        )

    tables = []
    for heading, filename, columns in [
        ("Source comparison", "summary.csv", ["scope", "threads", "runs", "avg_duration_seconds_per_run", "avg_tool_calls_per_run", "avg_served_input_tokens_per_run", "avg_output_tokens_per_run", "aggregate_cache_read_ratio"]),
        ("Model usage", "model_usage.csv", ["source", "model", "calls", "served_input_tokens", "output_tokens", "cache_read_ratio", "reasoning_share_of_output"]),
        ("Tool usage", "tool_usage.csv", ["source", "tool_name", "calls", "known_outcomes", "failures", "failure_ratio"]),
        ("Data quality", "data_quality.csv", None),
    ]:
        path = data_dir / filename
        if path.exists():
            tables.append(f"<h2>{html.escape(heading)}</h2><div class=\"panel\">{render_table(pd.read_csv(path), columns)}</div>")

    generated = datetime.now().astimezone().strftime("%Y-%m-%d %H:%M %Z")
    document = f"""<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{html.escape(title)}</title><style>{BASE_CSS}</style></head><body><main>
<p class="eyebrow">Coding-agent analytics</p><h1>{html.escape(title)}</h1>
<p class="muted">Generated {html.escape(generated)} · offline report · transcript content remains on this machine</p>
<section class="cards">{cards_html}</section>
<h2>Charts</h2><section class="charts">{''.join(chart_html)}</section>
{''.join(tables)}
<h2>Transcripts</h2><div class="panel"><input id="transcript-search" type="search" placeholder="Filter by source or filename…" aria-label="Filter transcripts">
<table><thead><tr><th>Source</th><th>File</th><th>Entries</th><th>First timestamp</th><th>Last timestamp</th></tr></thead>
<tbody id="transcripts">{''.join(transcript_html)}</tbody></table></div>
</main><script>
const search = document.querySelector('#transcript-search');
search.addEventListener('input', () => {{
  const query = search.value.trim().toLowerCase();
  document.querySelectorAll('#transcripts tr').forEach(row => {{
    row.hidden = query && !row.dataset.search.includes(query);
  }});
}});
</script></body></html>"""
    index_path = output_dir / "index.html"
    index_path.write_text(document, encoding="utf-8", newline="\n")
    return index_path


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Convert Codex and Claude sessions, analyze them, and create an offline HTML report."
    )
    parser.add_argument("--codex-input", type=Path, default=Path.home() / ".codex" / "sessions")
    parser.add_argument("--claude-input", type=Path, default=Path.home() / ".claude" / "projects")
    parser.add_argument("--output-dir", type=Path, default=SCRIPT_DIR / "report")
    parser.add_argument("--title", default="Codex and Claude usage report")
    parser.add_argument("--open", action="store_true", help="Open the finished report in the default browser")
    args = parser.parse_args()

    codex_input = args.codex_input.expanduser().resolve()
    claude_input = args.claude_input.expanduser().resolve()
    output_dir = args.output_dir.expanduser().resolve()
    codex_paths = discover_jsonl(codex_input)
    claude_paths = discover_jsonl(claude_input)
    if not codex_paths and not claude_paths:
        parser.error(f"No JSONL transcripts found in {codex_input} or {claude_input}")

    output_dir.mkdir(parents=True, exist_ok=True)
    run_pipeline(codex_input, claude_input, output_dir)
    index_path = build_report(
        output_dir,
        [("codex", codex_paths), ("claude", claude_paths)],
        args.title,
    )
    print(f"Wrote HTML report to {index_path}")
    if args.open:
        webbrowser.open(index_path.as_uri())


if __name__ == "__main__":
    main()
