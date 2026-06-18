package bench

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
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
	DerivedFrom string             `json:"derived_from,omitempty"`
	Unavailable string             `json:"unavailable,omitempty"`
}

type RunSummary struct {
	Name       string             `json:"name"`
	ResultFile string             `json:"result_file"`
	Metrics    map[string]float64 `json:"metrics,omitempty"`
	Error      string             `json:"error,omitempty"`
}

type requestRecord struct {
	Metadata struct {
		SessionNum     int     `json:"session_num"`
		RequestStartNS float64 `json:"request_start_ns"`
		RequestEndNS   float64 `json:"request_end_ns"`
	} `json:"metadata"`
	Metrics map[string]requestMetric `json:"metrics"`
}

type requestMetric struct {
	Value any    `json:"value"`
	Unit  string `json:"unit"`
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
		if workload.ID == "overall" {
			derived, err := mixedWorkloadBreakdown(workloadDir, resultFiles[0], runs[0].Metrics)
			if err == nil {
				summary.Workloads = append(summary.Workloads, derived...)
			}
		}
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
	latencyKeys := timedDistributionMetricKeys()
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
		aggregate["tps"] += run.Metrics["tps"]
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
	if aggregate["tps"] == 0 && aggregate["output_token_throughput"] > 0 {
		aggregate["tps"] = aggregate["output_token_throughput"]
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

func mixedWorkloadBreakdown(workloadDir string, resultFile string, overallMetrics map[string]float64) ([]WorkloadSummary, error) {
	tracePath := filepath.Join(workloadDir, "trace.jsonl")
	workloadByIndex, err := readTraceWorkloads(tracePath)
	if err != nil {
		return nil, err
	}
	if len(workloadByIndex) == 0 {
		return nil, nil
	}

	recordsPath := filepath.Join(filepath.Dir(resultFile), "profile_export.jsonl")
	records, err := readRequestRecords(recordsPath)
	if err != nil {
		return nil, err
	}

	duration := overallMetrics["benchmark_duration"]
	if duration <= 0 {
		duration = requestRecordsDurationSeconds(records)
	}
	if duration <= 0 {
		duration = 1
	}

	grouped := map[string][]requestRecord{}
	for _, record := range records {
		if record.Metadata.SessionNum < 0 || record.Metadata.SessionNum >= len(workloadByIndex) {
			continue
		}
		workloadID := workloadByIndex[record.Metadata.SessionNum]
		if workloadID == "" {
			continue
		}
		grouped[workloadID] = append(grouped[workloadID], record)
	}

	ids := make([]string, 0, len(grouped))
	for id := range grouped {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	summaries := make([]WorkloadSummary, 0, len(ids))
	for _, id := range ids {
		metrics := metricsFromRequestRecords(grouped[id], duration)
		summaries = append(summaries, WorkloadSummary{
			ID:          id,
			ResultFile:  recordsPath,
			Metrics:     metrics,
			DerivedFrom: "overall",
			Runs: []RunSummary{
				{
					Name:       "derived-from-overall",
					ResultFile: recordsPath,
					Metrics:    metrics,
				},
			},
		})
	}
	return summaries, nil
}

func readTraceWorkloads(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var workloads []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry struct {
			Workload string `json:"_workload"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, err
		}
		workloads = append(workloads, entry.Workload)
	}
	return workloads, scanner.Err()
}

func readRequestRecords(path string) ([]requestRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var records []requestRecord
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record requestRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, scanner.Err()
}

func requestRecordsDurationSeconds(records []requestRecord) float64 {
	if len(records) == 0 {
		return 0
	}
	minStart := math.Inf(1)
	maxEnd := 0.0
	for _, record := range records {
		if record.Metadata.RequestStartNS > 0 && record.Metadata.RequestStartNS < minStart {
			minStart = record.Metadata.RequestStartNS
		}
		if record.Metadata.RequestEndNS > maxEnd {
			maxEnd = record.Metadata.RequestEndNS
		}
	}
	if math.IsInf(minStart, 1) || maxEnd <= minStart {
		return 0
	}
	return (maxEnd - minStart) / 1e9
}

func metricsFromRequestRecords(records []requestRecord, durationSeconds float64) map[string]float64 {
	metrics := map[string]float64{
		"request_count": float64(len(records)),
	}
	if len(records) == 0 {
		return metrics
	}
	if durationSeconds > 0 {
		metrics["request_throughput"] = float64(len(records)) / durationSeconds
	}

	addRequestMetricStats(metrics, records, "time_to_first_token", "ttft")
	addRequestMetricStats(metrics, records, "request_latency", "latency")
	addRequestMetricStats(metrics, records, "inter_token_latency", "tpot")
	addRequestMetricStats(metrics, records, "input_sequence_length", "input_sequence_length")
	addRequestMetricStats(metrics, records, "output_sequence_length", "output_sequence_length")

	outputTokens := sumRequestMetric(records, "output_token_count")
	if outputTokens == 0 {
		outputTokens = sumRequestMetric(records, "output_sequence_length")
	}
	if outputTokens > 0 {
		metrics["output_token_count"] = outputTokens
		if durationSeconds > 0 {
			metrics["output_token_throughput"] = outputTokens / durationSeconds
			metrics["tps"] = outputTokens / durationSeconds
		}
	}
	return metrics
}

func addRequestMetricStats(out map[string]float64, records []requestRecord, sourceKey string, outputPrefix string) {
	values := requestMetricValues(records, sourceKey)
	if len(values) == 0 {
		return
	}
	sort.Float64s(values)
	if outputPrefix == "input_sequence_length" || outputPrefix == "output_sequence_length" {
		out[outputPrefix+"_avg"] = mean(values)
		out[outputPrefix+"_p50"] = percentile(values, 0.50)
		out[outputPrefix+"_p95"] = percentile(values, 0.95)
		out[outputPrefix+"_p99"] = percentile(values, 0.99)
		return
	}

	out[outputPrefix+"_avg_ms"] = mean(values)
	out[outputPrefix+"_min_ms"] = values[0]
	out[outputPrefix+"_max_ms"] = values[len(values)-1]
	out[outputPrefix+"_p1_ms"] = percentile(values, 0.01)
	out[outputPrefix+"_p5_ms"] = percentile(values, 0.05)
	out[outputPrefix+"_p10_ms"] = percentile(values, 0.10)
	out[outputPrefix+"_p25_ms"] = percentile(values, 0.25)
	out[outputPrefix+"_p50_ms"] = percentile(values, 0.50)
	out[outputPrefix+"_p75_ms"] = percentile(values, 0.75)
	out[outputPrefix+"_p90_ms"] = percentile(values, 0.90)
	out[outputPrefix+"_p95_ms"] = percentile(values, 0.95)
	out[outputPrefix+"_p99_ms"] = percentile(values, 0.99)
	out[outputPrefix+"_std_ms"] = stddev(values)
}

func requestMetricValues(records []requestRecord, key string) []float64 {
	values := make([]float64, 0, len(records))
	for _, record := range records {
		if value, ok := requestMetricFloat(record.Metrics[key]); ok {
			values = append(values, value)
		}
	}
	return values
}

func sumRequestMetric(records []requestRecord, key string) float64 {
	sum := 0.0
	for _, record := range records {
		if value, ok := requestMetricFloat(record.Metrics[key]); ok {
			sum += value
		}
	}
	return sum
}

func requestMetricFloat(metric requestMetric) (float64, bool) {
	switch typed := metric.Value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	default:
		return 0, false
	}
}

func mean(values []float64) float64 {
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

func stddev(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	avg := mean(values)
	sum := 0.0
	for _, value := range values {
		delta := value - avg
		sum += delta * delta
	}
	return math.Sqrt(sum / float64(len(values)))
}

func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	position := q * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sorted[lower]
	}
	weight := position - float64(lower)
	return sorted[lower]*(1-weight) + sorted[upper]*weight
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
	copyMetricAverage(metrics, root, "output_token_throughput", "tps")
	copyMetricAverage(metrics, root, "request_count", "request_count")
	copyMetricDistribution(metrics, root, "time_to_first_token", "ttft")
	copyMetricDistribution(metrics, root, "request_latency", "latency")
	copyMetricDistribution(metrics, root, "inter_token_latency", "tpot")
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

func copyMetricDistribution(out map[string]float64, root map[string]any, sourceKey string, outputPrefix string) {
	metric, ok := root[sourceKey].(map[string]any)
	if !ok {
		return
	}
	for _, stat := range distributionStats() {
		value, ok := metric[stat].(float64)
		if !ok {
			continue
		}
		out[outputPrefix+"_"+stat+"_ms"] = value
	}
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
		"tps":                     {"output token throughput", "tokens/sec", "tps"},
		"request_count":           {"request count", "request_count", "requestcount"},
	}
	for _, metric := range []struct {
		prefix string
		source string
		label  string
	}{
		{"ttft", "time_to_first_token", "time to first token"},
		{"latency", "request_latency", "request latency"},
		{"tpot", "inter_token_latency", "inter token latency"},
	} {
		for _, stat := range distributionStats() {
			key := metric.prefix + "_" + stat + "_ms"
			preferred[key] = []string{
				metric.label + " " + stat,
				metric.prefix + " " + stat,
				metric.source + "." + stat,
			}
		}
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
		} else if workload.DerivedFrom != "" {
			status = "derived from " + workload.DerivedFrom
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

	latencyKeys := timedDistributionMetricKeys()
	latencySums := map[string]float64{}
	latencyCounts := map[string]int{}

	for _, workload := range workloads {
		if workload.Unavailable == "" && len(workload.Metrics) > 0 {
			overall.CompletedWorkloads++
		}
		if workload.DerivedFrom != "" {
			continue
		}
		for _, run := range workload.Runs {
			if run.Error != "" || len(run.Metrics) == 0 {
				continue
			}
			overall.CompletedRuns++
			overall.TotalRequestCount += run.Metrics["request_count"]
			overall.Metrics["request_throughput"] += run.Metrics["request_throughput"]
			overall.Metrics["tps"] += run.Metrics["tps"]
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
	if overall.Metrics["tps"] == 0 && overall.Metrics["output_token_throughput"] > 0 {
		overall.Metrics["tps"] = overall.Metrics["output_token_throughput"]
	}
	for key, value := range overall.Metrics {
		if value == 0 {
			delete(overall.Metrics, key)
		}
	}
	return overall
}

func distributionStats() []string {
	return []string{"avg", "min", "max", "p1", "p5", "p10", "p25", "p50", "p75", "p90", "p95", "p99", "std"}
}

func timedDistributionMetricKeys() []string {
	prefixes := []string{"ttft", "latency", "tpot"}
	stats := distributionStats()
	keys := make([]string, 0, len(prefixes)*len(stats))
	for _, prefix := range prefixes {
		for _, stat := range stats {
			keys = append(keys, prefix+"_"+stat+"_ms")
		}
	}
	return keys
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
