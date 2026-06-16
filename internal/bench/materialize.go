package bench

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type MaterializedWorkload struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Category        string       `json:"category"`
	Description     string       `json:"description"`
	Pairing         string       `json:"pairing"`
	MetricsFocus    []string     `json:"metrics_focus"`
	MaxOutputTokens int          `json:"max_output_tokens"`
	DefaultRequests int          `json:"default_requests"`
	Phase           Phase        `json:"phase"`
	TrafficSources  []DataSource `json:"traffic_sources"`
	PromptSources   []DataSource `json:"prompt_sources"`
	BundledData     CorpusInfo   `json:"bundled_data"`
	Harness         HarnessHint  `json:"harness"`
}

type HarnessHint struct {
	Name        string   `json:"name"`
	Notes       string   `json:"notes"`
	CommandHint []string `json:"command_hint"`
}

type AIPerfTraceEntry struct {
	Source      string `json:"_source,omitempty"`
	Timestamp   int    `json:"timestamp"`
	InputLength int    `json:"input_length,omitempty"`
	InputText   string `json:"input_text,omitempty"`
	HashIDs     []int  `json:"hash_ids,omitempty"`
}

type MaterializeOptions struct {
	TraceDir  string
	PromptDir string
}

const bundledPromptDir = "data/bundled/prompts"
const bundledTraceDir = "data/bundled/traces"

func MaterializeSuite(suite Suite, outDir string) error {
	return MaterializeSuiteWithOptions(suite, outDir, MaterializeOptions{})
}

func MaterializeSuiteWithOptions(suite Suite, outDir string, opts MaterializeOptions) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	for _, workload := range suite.Workloads {
		dir := filepath.Join(outDir, workload.ID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}

		materialized := MaterializedWorkload{
			ID:              workload.ID,
			Name:            workload.Name,
			Category:        workload.Category,
			Description:     workload.Description,
			Pairing:         workload.Pairing,
			MetricsFocus:    workload.MetricsFocus,
			MaxOutputTokens: workload.MaxOutputTokens,
			DefaultRequests: workload.DefaultRequests,
			Phase:           workload.Phase,
			TrafficSources:  workload.TrafficSources,
			PromptSources:   workload.PromptSources,
			BundledData:     corpusInfo(workload),
			Harness:         aiperfHint(workload),
		}

		data, err := json.MarshalIndent(materialized, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "workload.json"), append(data, '\n'), 0o644); err != nil {
			return err
		}
		tracePath := filepath.Join(dir, "trace.jsonl")
		if err := writeOrCopyTrace(tracePath, workload, opts.TraceDir); err != nil {
			return err
		}
		if err := applyPromptOverride(tracePath, workload, opts.PromptDir); err != nil {
			return err
		}
		if err := writeOrCopyPool(filepath.Join(dir, "pool.jsonl"), workload, opts.PromptDir); err != nil {
			return err
		}
		for _, multiplier := range loadMultipliers(suite.Sweeps) {
			name := fmt.Sprintf("trace-load-%.2fx.jsonl", multiplier)
			if err := writeScaledAIPerfTrace(filepath.Join(dir, name), tracePath, multiplier); err != nil {
				return err
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "aiperf.yaml"), []byte(aiperfYAML(workload)), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(aiperfShell(workload)), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "sweep-load.sh"), []byte(aiperfLoadSweepShell(workload, suite.Sweeps)), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "sweep-throughput.sh"), []byte(aiperfThroughputSweepShell(workload, suite.Sweeps)), 0o755); err != nil {
			return err
		}
	}

	index, err := json.MarshalIndent(suite, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "suite.json"), append(index, '\n'), 0o644)
}

func applyPromptOverride(tracePath string, workload Workload, promptDir string) error {
	return nil
}

func padPromptToApproxTokens(prompt string, targetTokens int) string {
	if targetTokens <= 0 {
		return prompt
	}
	current := approximateTokenCount(prompt)
	if current >= targetTokens {
		return prompt
	}

	var b strings.Builder
	b.WriteString(prompt)
	for current < targetTokens {
		b.WriteString("\n\nContext filler: this benchmark padding preserves the requested long-context input length while keeping the task text deterministic and non-sensitive.")
		current += 19
	}
	return b.String()
}

func writeOrCopyPool(path string, workload Workload, promptDir string) error {
	prompts, err := workloadPrompts(workload, promptDir)
	if err != nil {
		return err
	}
	if len(prompts) == 0 {
		return writeAIPerfPool(path, workload)
	}

	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	for i := 0; i < max(1, workload.DefaultRequests); i++ {
		text := prompts[i%len(prompts)]
		if workload.Category == "long-context" {
			text = padPromptToApproxTokens(text, syntheticInputLength(i, workload.Category))
		}
		entry := map[string]any{
			"text":          text,
			"output_length": maxOutputTokens(workload),
			"extra": map[string]any{
				"temperature": 0,
			},
		}
		data, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if _, err := writer.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func workloadPrompts(workload Workload, promptDir string) ([]string, error) {
	if promptDir == "" {
		promptDir = bundledPromptDir
	}
	path := filepath.Join(promptDir, workload.ID+".jsonl")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return readPromptTexts(path, workload.Category)
}

func readTraceEntries(path string) ([]AIPerfTraceEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var entries []AIPerfTraceEntry
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry AIPerfTraceEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, scanner.Err()
}

func writeTraceEntries(path string, entries []AIPerfTraceEntry) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	for _, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if _, err := writer.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func writeOrCopyTrace(path string, workload Workload, traceDir string) error {
	if traceDir == "" {
		bundled := filepath.Join(bundledTraceDir, workload.ID+".jsonl")
		if _, err := os.Stat(bundled); err == nil {
			return writeExpandedBundledTrace(path, bundled, workload)
		} else if !os.IsNotExist(err) {
			return err
		}
		return writeAIPerfTrace(path, workload)
	}

	override := filepath.Join(traceDir, workload.ID+".jsonl")
	if _, err := os.Stat(override); err == nil {
		return copyFile(override, path)
	} else if !os.IsNotExist(err) {
		return err
	}

	return writeAIPerfTrace(path, workload)
}

func writeExpandedBundledTrace(path string, sourcePath string, workload Workload) error {
	entries, err := readTraceEntries(sourcePath)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("bundled trace %s has no rows", sourcePath)
	}
	if workload.Phase.TraceMode == "source_window" {
		if workload.Phase.WindowSeconds > 0 {
			entries = selectTraceWindow(entries, workload.Phase.WindowSeconds*1000, workload.Phase.Selection)
		} else {
			entries = append([]AIPerfTraceEntry(nil), entries...)
			normalizeTraceTimestamps(entries)
		}
		return writeTraceEntries(path, entries)
	}

	target := workload.DefaultRequests
	if target <= 0 {
		target = len(entries)
	}
	if len(entries) >= target {
		return writeTraceEntries(path, entries[:target])
	}

	period := bundledTracePeriod(entries, workload)
	expanded := make([]AIPerfTraceEntry, 0, target)
	for len(expanded) < target {
		cycle := len(expanded) / len(entries)
		for _, entry := range entries {
			if len(expanded) >= target {
				break
			}
			next := entry
			next.Timestamp = entry.Timestamp + cycle*period
			expanded = append(expanded, next)
		}
	}
	return writeTraceEntries(path, expanded)
}

func bundledTracePeriod(entries []AIPerfTraceEntry, workload Workload) int {
	last := entries[len(entries)-1].Timestamp
	if last <= 0 {
		if workload.Phase.DurationSeconds > 0 {
			return workload.Phase.DurationSeconds * 1000
		}
		return len(entries) * 1000
	}
	if len(entries) == 1 {
		return last + 1000
	}
	gap := last - entries[len(entries)-2].Timestamp
	if gap <= 0 {
		gap = 1000
	}
	return last + gap
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func aiperfHint(workload Workload) HarnessHint {
	return HarnessHint{
		Name:  "aiperf",
		Notes: "Uses AIPerf fixed-schedule replay with mooncake_trace JSONL. Trace rows carry timestamp, input length or text, and optional hash ids. Output length is controlled only by a workload-level cap.",
		CommandHint: []string{
			"aiperf",
			"profile",
			"--config",
			fmt.Sprintf("out/default/%s/aiperf.yaml", workload.ID),
		},
	}
}

func writeAIPerfTrace(path string, workload Workload) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	for _, entry := range sampleTrace(workload) {
		data, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if _, err := writer.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func writeScaledAIPerfTrace(path string, sourcePath string, multiplier float64) error {
	if multiplier <= 0 {
		multiplier = 1
	}

	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()

	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 1024), 16*1024*1024)
	writer := bufio.NewWriter(file)
	for scanner.Scan() {
		var entry AIPerfTraceEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return err
		}
		entry.Timestamp = int(float64(entry.Timestamp) / multiplier)
		data, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if _, err := writer.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return writer.Flush()
}

func writeAIPerfPool(path string, workload Workload) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	for i := 0; i < max(1, workload.DefaultRequests); i++ {
		length := syntheticInputLength(i, workload.Category)
		text := syntheticPrompt(i, workload.Category, length)
		entry := map[string]any{
			"text":          text,
			"output_length": maxOutputTokens(workload),
			"extra": map[string]any{
				"temperature": 0,
			},
		}
		data, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if _, err := writer.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func sampleTrace(workload Workload) []AIPerfTraceEntry {
	requests := workload.DefaultRequests
	if requests <= 0 {
		requests = 24
	}
	duration := workload.Phase.DurationSeconds
	if duration <= 0 {
		duration = 120
	}

	entries := make([]AIPerfTraceEntry, 0, requests)
	for i := 0; i < requests; i++ {
		ts := syntheticTimestampMillis(i, requests, duration, workload.Category)
		length := syntheticInputLength(i, workload.Category)
		entry := AIPerfTraceEntry{
			Timestamp:   ts,
			InputLength: length,
			HashIDs:     syntheticHashIDs(i, workload.Category, length),
		}
		if workload.Category == "code" {
			entry.InputText = syntheticCodePrompt(i, length)
			entry.InputLength = 0
		}
		entries = append(entries, entry)
	}
	return entries
}

func syntheticTimestampMillis(index, count, durationSeconds int, category string) int {
	if count <= 1 {
		return 0
	}
	baseGap := durationSeconds * 1000 / count
	ts := index * baseGap

	switch category {
	case "production-chat":
		if index%17 >= 12 {
			return ts / 2
		}
		return ts
	case "cache", "rag", "agentic", "stateful-cache-agent":
		return ts + (index%4)*1800
	case "long-context":
		return index * 10000
	default:
		return ts
	}
}

func syntheticInputLength(index int, category string) int {
	switch category {
	case "production-chat":
		return []int{96, 220, 480, 900, 1600, 3200, 640}[index%7]
	case "cache":
		return []int{512, 768, 1024, 1536, 2048, 4096}[index%6]
	case "rag":
		return []int{1800, 2400, 3200, 4200, 5200, 7000}[index%6]
	case "stateful-cache-agent":
		return []int{512, 900, 1800, 3200, 5200, 7000}[index%6]
	case "code", "code-repo":
		return []int{1200, 2400, 5200, 9000, 14000}[index%5]
	case "long-context":
		return []int{32000, 40000, 48000, 64000}[index%4]
	case "agentic":
		return []int{700, 1200, 2000, 3500, 5500}[index%5]
	case "synthetic-control":
		return []int{256, 512, 1024, 2048}[index%4]
	default:
		return 512
	}
}

func syntheticHashIDs(index int, category string, inputLength int) []int {
	blockCount := inputLength / 512
	if inputLength%512 != 0 {
		blockCount++
	}
	if blockCount <= 0 {
		blockCount = 1
	}

	var ids []int
	switch category {
	case "cache":
		group := index % 8
		ids = []int{1000 + group, 2000 + group, 3000 + index}
	case "rag":
		ids = []int{4001, 4100 + index%6, 5000 + index%12}
	case "agentic":
		session := index / 4
		ids = []int{6000 + session, 7000 + index%4}
	case "stateful-cache-agent":
		session := index / 4
		if index%3 == 0 {
			ids = []int{4001, 4100 + index%6, 6000 + session, 9000 + index}
		} else {
			ids = []int{6000 + session, 9000 + index}
		}
	case "synthetic-control":
		if index%2 == 0 {
			ids = []int{8001, 8002, 8003, 9000 + index}
		} else {
			ids = []int{9000 + index}
		}
	default:
		return nil
	}
	if len(ids) > blockCount {
		return ids[:blockCount]
	}
	for len(ids) < blockCount {
		ids = append(ids, 100000+index*100+len(ids))
	}
	return ids
}

func syntheticCodePrompt(index, length int) string {
	return fmt.Sprintf("You are editing a production Go service. The approximate input budget for this task is %d tokens.\n\nFile: service_%03d.go\n\nFix the latency regression in the request scheduler. Preserve behavior and keep the patch small.\n\n```go\npackage service\n\nfunc HandleRequest() error {\n\t// Existing code omitted in this sample fixture.\n\treturn nil\n}\n```\n", length, index)
}

func syntheticPrompt(index int, category string, length int) string {
	if category == "code" || category == "code-repo" {
		return syntheticCodePrompt(index, length)
	}
	return fmt.Sprintf("Synthetic %s benchmark request %03d. Approximate input token budget: %d. Respond briefly and deterministically.", category, index, length)
}

func aiperfYAML(workload Workload) string {
	return fmt.Sprintf(`schemaVersion: "2.0"
random_seed: 42

benchmark:
  model: ${MODEL_NAME:Qwen/Qwen3-0.6B}
  endpoint:
    url: ${INFERENCE_URL:http://localhost:8000}
    type: chat
    streaming: true
  dataset:
    type: file
    format: mooncake_trace
    path: trace.jsonl
  phases:
    - name: profiling
      type: fixed_schedule
      auto_offset: true
  generation:
    max_tokens: %d
    temperature: 0
  artifacts:
    dir: ${ARTIFACT_DIR:artifacts/%s}
`, maxOutputTokens(workload), workload.ID)
}

func aiperfShell(workload Workload) string {
	return fmt.Sprintf(`#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")"
${AIPERF_BIN:-aiperf} profile \
  --model "${MODEL_NAME:-Qwen/Qwen3-0.6B}" \
  --endpoint-type chat \
  --streaming \
  --url "${INFERENCE_URL:-http://localhost:8000}" \
  --input-file trace.jsonl \
  --custom-dataset-type mooncake_trace \
  --fixed-schedule \
  --fixed-schedule-auto-offset \
  --osl %d \
  --artifact-dir "${ARTIFACT_DIR:-artifacts/%s}" \
  --extra-inputs temperature:0
`, maxOutputTokens(workload), workload.ID)
}

func aiperfLoadSweepShell(workload Workload, sweeps []Sweep) string {
	script := `#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")"
`
	for _, multiplier := range loadMultipliers(sweeps) {
		traceFile := fmt.Sprintf("trace-load-%.2fx.jsonl", multiplier)
		script += fmt.Sprintf(`
echo "load sweep multiplier %.2fx"
${AIPERF_BIN:-aiperf} profile \
  --model "${MODEL_NAME:-Qwen/Qwen3-0.6B}" \
  --endpoint-type chat \
  --streaming \
  --url "${INFERENCE_URL:-http://localhost:8000}" \
  --input-file %s \
  --custom-dataset-type mooncake_trace \
  --fixed-schedule \
  --fixed-schedule-auto-offset \
  --osl %d \
  --artifact-dir "${ARTIFACT_DIR:-artifacts/%s/load-%.2fx}" \
  --extra-inputs temperature:0
`, multiplier, traceFile, maxOutputTokens(workload), workload.ID, multiplier)
	}
	return script
}

func loadMultipliers(sweeps []Sweep) []float64 {
	for _, sweep := range sweeps {
		if sweep.ID == "load" && len(sweep.Multipliers) > 0 {
			return sweep.Multipliers
		}
	}
	return []float64{0.5, 1, 1.5, 2, 3}
}

func aiperfThroughputSweepShell(workload Workload, sweeps []Sweep) string {
	concurrency := []int{1, 2, 4, 8, 16, 32, 64}
	for _, sweep := range sweeps {
		if sweep.ID == "throughput" && len(sweep.Concurrency) > 0 {
			concurrency = sweep.Concurrency
			break
		}
	}

	script := `#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")"
`
	for _, c := range concurrency {
		script += fmt.Sprintf(`
echo "throughput sweep concurrency %d"
${AIPERF_BIN:-aiperf} profile \
  --model "${MODEL_NAME:-Qwen/Qwen3-0.6B}" \
  --endpoint-type chat \
  --streaming \
  --url "${INFERENCE_URL:-http://localhost:8000}" \
  --input-file pool.jsonl \
  --custom-dataset-type random_pool \
  --dataset-sampling-strategy random \
  --concurrency %d \
  --request-count %d \
  --osl %d \
  --artifact-dir "${ARTIFACT_DIR:-artifacts/%s/throughput-c%d}" \
  --extra-inputs temperature:0
`, c, c, workload.DefaultRequests, maxOutputTokens(workload), workload.ID, c)
	}
	return script
}

func maxOutputTokens(workload Workload) int {
	if workload.MaxOutputTokens > 0 {
		return workload.MaxOutputTokens
	}
	return 256
}
