package bench

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type SuiteSummary struct {
	ArtifactDir string            `json:"artifact_dir"`
	Overall     OverallSummary    `json:"overall"`
	Workloads   []WorkloadSummary `json:"workloads"`
}

type OverallSummary struct {
	CompletedWorkloads int                `json:"completed_workloads"`
	TotalWorkloads     int                `json:"total_workloads"`
	CompletedRuns      int                `json:"completed_runs"`
	TotalRequestCount  float64            `json:"total_request_count,omitempty"`
	Metrics            map[string]float64 `json:"metrics,omitempty"`
}

type WorkloadSummary struct {
	ID          string             `json:"id"`
	ResultFile  string             `json:"result_file,omitempty"`
	Metrics     map[string]float64 `json:"metrics,omitempty"`
	Runs        []RunSummary       `json:"runs,omitempty"`
	Unavailable string             `json:"unavailable,omitempty"`
}

type RunSummary struct {
	Name       string             `json:"name"`
	ResultFile string             `json:"result_file"`
	Metrics    map[string]float64 `json:"metrics,omitempty"`
	Error      string             `json:"error,omitempty"`
}

func SummarizeResults(suite Suite, outDir string) (SuiteSummary, error) {
	summary := SuiteSummary{
		ArtifactDir: outDir,
		Workloads:   make([]WorkloadSummary, 0, len(suite.Workloads)),
	}

	for _, workload := range suite.Workloads {
		workloadDir := filepath.Join(outDir, workload.ID)
		resultFiles, err := findAIPerfJSON(workloadDir)
		if err != nil {
			return SuiteSummary{}, err
		}
		if len(resultFiles) == 0 {
			summary.Workloads = append(summary.Workloads, WorkloadSummary{
				ID:          workload.ID,
				Unavailable: "no AIPerf JSON export found",
			})
			continue
		}

		runs := make([]RunSummary, 0, len(resultFiles))
		for _, resultFile := range resultFiles {
			metrics, err := extractMetrics(resultFile)
			run := RunSummary{
				Name:       runName(workloadDir, resultFile),
				ResultFile: resultFile,
				Metrics:    metrics,
			}
			if err != nil {
				run.Error = err.Error()
			}
			runs = append(runs, run)
		}

		metrics := aggregateRunMetrics(runs)
		if len(metrics) == 0 {
			primary := runs[0]
			summary.Workloads = append(summary.Workloads, WorkloadSummary{
				ID:          workload.ID,
				ResultFile:  primary.ResultFile,
				Runs:        runs,
				Unavailable: firstRunError(runs),
			})
			continue
		}
		summary.Workloads = append(summary.Workloads, WorkloadSummary{
			ID:         workload.ID,
			ResultFile: runs[0].ResultFile,
			Metrics:    metrics,
			Runs:       runs,
		})
	}

	summary.Overall = overallSummary(summary.Workloads)
	return summary, nil
}

func firstRunError(runs []RunSummary) string {
	for _, run := range runs {
		if run.Error != "" {
			return run.Error
		}
	}
	return "no usable AIPerf metrics found"
}

func aggregateRunMetrics(runs []RunSummary) map[string]float64 {
	aggregate := map[string]float64{}
	latencyKeys := []string{"ttft_p50_ms", "ttft_p95_ms", "ttft_p99_ms", "latency_p50_ms", "latency_p95_ms", "latency_p99_ms"}
	latencySums := map[string]float64{}
	latencyCounts := map[string]int{}
	count := 0

	for _, run := range runs {
		if run.Error != "" || len(run.Metrics) == 0 {
			continue
		}
		count++
		aggregate["request_count"] += run.Metrics["request_count"]
		aggregate["request_throughput"] += run.Metrics["request_throughput"]
		aggregate["output_token_throughput"] += run.Metrics["output_token_throughput"]
		for _, key := range latencyKeys {
			if value, ok := run.Metrics[key]; ok {
				latencySums[key] += value
				latencyCounts[key]++
			}
		}
	}
	if count == 0 {
		return nil
	}

	for _, key := range latencyKeys {
		if latencyCounts[key] > 0 {
			aggregate["mean_"+key] = latencySums[key] / float64(latencyCounts[key])
		}
	}
	for key, value := range aggregate {
		if value == 0 {
			delete(aggregate, key)
		}
	}
	return aggregate
}

func WriteSummaryFiles(summary SuiteSummary, outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	jsonData, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "summary.json"), append(jsonData, '\n'), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "summary.md"), []byte(summaryMarkdown(summary)), 0o644)
}

func PrintSummary(summary SuiteSummary) string {
	return summaryMarkdown(summary)
}

func findAIPerfJSON(root string) ([]string, error) {
	var matches []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if name == "profile_export_aiperf.json" || strings.HasSuffix(name, "_aiperf.json") {
			matches = append(matches, path)
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

func runName(workloadDir, resultFile string) string {
	rel, err := filepath.Rel(workloadDir, filepath.Dir(resultFile))
	if err != nil || rel == "." {
		return "default"
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 0 {
		return "default"
	}
	return parts[len(parts)-1]
}

func extractMetrics(path string) (map[string]float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}

	if metrics := knownAIPerfMetrics(value); len(metrics) > 0 {
		return metrics, nil
	}

	found := map[string]float64{}
	walkMetrics(value, "", found)
	if len(found) == 0 {
		return nil, fmt.Errorf("no numeric metrics found")
	}

	return selectMetrics(found), nil
}

func knownAIPerfMetrics(value any) map[string]float64 {
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}

	metrics := map[string]float64{}
	copyMetricAverage(metrics, root, "request_throughput", "request_throughput")
	copyMetricAverage(metrics, root, "output_token_throughput", "output_token_throughput")
	copyMetricAverage(metrics, root, "request_count", "request_count")
	copyMetricPercentile(metrics, root, "time_to_first_token", "ttft_p50_ms", "p50")
	copyMetricPercentile(metrics, root, "time_to_first_token", "ttft_p95_ms", "p95")
	copyMetricPercentile(metrics, root, "time_to_first_token", "ttft_p99_ms", "p99")
	copyMetricPercentile(metrics, root, "request_latency", "latency_p50_ms", "p50")
	copyMetricPercentile(metrics, root, "request_latency", "latency_p95_ms", "p95")
	copyMetricPercentile(metrics, root, "request_latency", "latency_p99_ms", "p99")
	copyMetricAverage(metrics, root, "input_sequence_length", "input_sequence_length_avg")
	copyMetricAverage(metrics, root, "output_sequence_length", "output_sequence_length_avg")
	return metrics
}

func copyMetricAverage(out map[string]float64, root map[string]any, sourceKey string, outputKey string) {
	copyMetricPercentile(out, root, sourceKey, outputKey, "avg")
}

func copyMetricPercentile(out map[string]float64, root map[string]any, sourceKey string, outputKey string, stat string) {
	metric, ok := root[sourceKey].(map[string]any)
	if !ok {
		return
	}
	value, ok := metric[stat].(float64)
	if !ok {
		return
	}
	out[outputKey] = value
}

func walkMetrics(value any, path string, found map[string]float64) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			next := key
			if path != "" {
				next = path + "." + key
			}
			walkMetrics(child, next, found)
		}
	case []any:
		for index, child := range typed {
			walkMetrics(child, fmt.Sprintf("%s.%d", path, index), found)
		}
	case float64:
		normalized := normalizeMetricName(path)
		if wantedMetric(normalized) {
			found[normalized] = typed
		}
	}
}

func selectMetrics(found map[string]float64) map[string]float64 {
	selected := map[string]float64{}
	preferred := map[string][]string{
		"request_throughput":      {"request throughput", "request_throughput", "requestthroughput", "requests/sec"},
		"output_token_throughput": {"output token throughput", "output_token_throughput", "outputtokenthroughput", "tokens/sec"},
		"ttft_p50_ms":             {"time to first token p50", "ttft p50", "time_to_first_token.p50"},
		"ttft_p95_ms":             {"time to first token p95", "ttft p95", "time_to_first_token.p95"},
		"ttft_p99_ms":             {"time to first token p99", "ttft p99", "time_to_first_token.p99"},
		"latency_p50_ms":          {"request latency p50", "latency p50", "request_latency.p50"},
		"latency_p95_ms":          {"request latency p95", "latency p95", "request_latency.p95"},
		"latency_p99_ms":          {"request latency p99", "latency p99", "request_latency.p99"},
		"request_count":           {"request count", "request_count", "requestcount"},
	}

	for output, candidates := range preferred {
		if value, ok := bestMetric(found, candidates); ok {
			selected[output] = value
		}
	}

	if len(selected) == 0 {
		for key, value := range found {
			selected[key] = value
			if len(selected) >= 12 {
				break
			}
		}
	}
	return selected
}

func bestMetric(found map[string]float64, candidates []string) (float64, bool) {
	for _, candidate := range candidates {
		normalizedCandidate := normalizeMetricName(candidate)
		for key, value := range found {
			if strings.Contains(key, normalizedCandidate) || strings.Contains(normalizedCandidate, key) {
				return value, true
			}
		}
	}
	return 0, false
}

func wantedMetric(name string) bool {
	keywords := []string{
		"throughput",
		"latency",
		"first token",
		"ttft",
		"inter token",
		"tpot",
		"request count",
		"input sequence",
		"output sequence",
	}
	for _, keyword := range keywords {
		if strings.Contains(name, keyword) {
			return true
		}
	}
	return false
}

func normalizeMetricName(name string) string {
	replacer := strings.NewReplacer("_", " ", "-", " ", ".", " ", "(", " ", ")", " ", "/", " ")
	return strings.Join(strings.Fields(strings.ToLower(replacer.Replace(name))), " ")
}

func summaryMarkdown(summary SuiteSummary) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# lm-serve-bench summary\n\n")
	fmt.Fprintf(&buf, "Artifacts: `%s`\n\n", summary.ArtifactDir)
	fmt.Fprintf(&buf, "## Overall\n\n")
	fmt.Fprintf(&buf, "- Completed workloads: %d/%d\n", summary.Overall.CompletedWorkloads, summary.Overall.TotalWorkloads)
	fmt.Fprintf(&buf, "- Completed runs: %d\n", summary.Overall.CompletedRuns)
	if summary.Overall.TotalRequestCount > 0 {
		fmt.Fprintf(&buf, "- Total request count: %.0f\n", summary.Overall.TotalRequestCount)
	}
	if len(summary.Overall.Metrics) > 0 {
		fmt.Fprintf(&buf, "- Aggregate metrics: %s\n", formatMetrics(summary.Overall.Metrics))
	}
	fmt.Fprintf(&buf, "\n## Workloads\n\n")
	fmt.Fprintf(&buf, "| Workload | Status | Key metrics |\n")
	fmt.Fprintf(&buf, "|---|---|---|\n")
	for _, workload := range summary.Workloads {
		status := "ok"
		metrics := formatMetrics(workload.Metrics)
		if workload.Unavailable != "" {
			status = workload.Unavailable
			metrics = ""
		} else if len(workload.Runs) > 1 {
			metrics = fmt.Sprintf("%d runs<br>%s", len(workload.Runs), metrics)
		}
		fmt.Fprintf(&buf, "| `%s` | %s | %s |\n", workload.ID, status, metrics)
	}
	return buf.String()
}

func overallSummary(workloads []WorkloadSummary) OverallSummary {
	overall := OverallSummary{
		TotalWorkloads: len(workloads),
		Metrics:        map[string]float64{},
	}

	latencyKeys := []string{"ttft_p50_ms", "ttft_p95_ms", "ttft_p99_ms", "latency_p50_ms", "latency_p95_ms", "latency_p99_ms"}
	latencySums := map[string]float64{}
	latencyCounts := map[string]int{}

	for _, workload := range workloads {
		if workload.Unavailable == "" && len(workload.Metrics) > 0 {
			overall.CompletedWorkloads++
		}
		for _, run := range workload.Runs {
			if run.Error != "" || len(run.Metrics) == 0 {
				continue
			}
			overall.CompletedRuns++
			overall.TotalRequestCount += run.Metrics["request_count"]
			overall.Metrics["request_throughput"] += run.Metrics["request_throughput"]
			overall.Metrics["output_token_throughput"] += run.Metrics["output_token_throughput"]
			for _, key := range latencyKeys {
				if value, ok := run.Metrics[key]; ok {
					latencySums[key] += value
					latencyCounts[key]++
				}
			}
		}
	}

	for _, key := range latencyKeys {
		if latencyCounts[key] > 0 {
			overall.Metrics["mean_"+key] = latencySums[key] / float64(latencyCounts[key])
		}
	}
	for key, value := range overall.Metrics {
		if value == 0 {
			delete(overall.Metrics, key)
		}
	}
	return overall
}

func formatMetrics(metrics map[string]float64) string {
	if len(metrics) == 0 {
		return ""
	}
	keys := make([]string, 0, len(metrics))
	for key := range metrics {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%.2f", key, metrics[key]))
	}
	return strings.Join(parts, "<br>")
}
