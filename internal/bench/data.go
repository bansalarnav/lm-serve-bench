package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type CorpusInfo struct {
	Mode          string         `json:"mode"`
	TraceFile     string         `json:"trace_file,omitempty"`
	TraceRecords  int            `json:"trace_records,omitempty"`
	Requests      int            `json:"requests,omitempty"`
	TraceSources  map[string]int `json:"trace_sources,omitempty"`
	PromptFile    string         `json:"prompt_file,omitempty"`
	PromptRecords int            `json:"prompt_records,omitempty"`
	PromptSources map[string]int `json:"prompt_sources,omitempty"`
	Notes         string         `json:"notes,omitempty"`
}

type CorpusReport struct {
	Manifest  []ManifestSourceReport `json:"manifest,omitempty"`
	Workloads []CorpusWorkloadReport `json:"workloads"`
}

type SourceManifest struct {
	Version     int              `json:"version"`
	Description string           `json:"description"`
	Sources     []ManifestSource `json:"sources"`
}

type ManifestSource struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Workload string `json:"workload"`
	File     string `json:"file"`
	Notes    string `json:"notes"`
}

type ManifestSourceReport struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Workload string `json:"workload"`
	File     string `json:"file,omitempty"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

type CorpusWorkloadReport struct {
	ID              string         `json:"id"`
	Mode            string         `json:"mode"`
	TraceFile       string         `json:"trace_file,omitempty"`
	TraceRecords    int            `json:"trace_records,omitempty"`
	Requests        int            `json:"requests,omitempty"`
	TraceSources    map[string]int `json:"trace_sources,omitempty"`
	PromptFile      string         `json:"prompt_file,omitempty"`
	PromptRecords   int            `json:"prompt_records,omitempty"`
	RenderedPrompts int            `json:"rendered_prompts,omitempty"`
	PromptSources   map[string]int `json:"prompt_sources,omitempty"`
	Notes           string         `json:"notes,omitempty"`
	Error           string         `json:"error,omitempty"`
}

func ValidateBundledData(suite Suite) CorpusReport {
	report := CorpusReport{
		Workloads: make([]CorpusWorkloadReport, 0, len(suite.Workloads)),
	}
	manifest, err := LoadSourceManifest()
	if err != nil {
		report.Manifest = append(report.Manifest, ManifestSourceReport{
			Status: "error",
			Error:  err.Error(),
		})
	} else {
		report.Manifest = validateManifestSources(manifest)
	}
	for _, workload := range suite.Workloads {
		info := corpusInfo(workload)
		item := CorpusWorkloadReport{
			ID:            workload.ID,
			Mode:          info.Mode,
			TraceFile:     info.TraceFile,
			TraceRecords:  info.TraceRecords,
			Requests:      info.Requests,
			TraceSources:  info.TraceSources,
			PromptFile:    info.PromptFile,
			PromptRecords: info.PromptRecords,
			PromptSources: info.PromptSources,
			Notes:         info.Notes,
		}
		if info.TraceFile != "" && info.TraceRecords == 0 {
			item.Error = "bundled trace has no rows"
		}
		if requiresBundledTrace(workload) && info.TraceFile == "" {
			item.Error = "missing bundled trace"
		}
		if info.PromptFile != "" {
			prompts, err := readPromptTexts(info.PromptFile, workload.Category)
			if err != nil {
				item.Error = err.Error()
			} else {
				item.RenderedPrompts = len(prompts)
				if len(prompts) == 0 {
					item.Error = "no renderable prompts"
				}
			}
		}
		if requiresBundledPrompts(workload) && info.PromptFile == "" {
			item.Error = "missing bundled prompt corpus"
		}
		report.Workloads = append(report.Workloads, item)
	}
	return report
}

func LoadSourceManifest() (SourceManifest, error) {
	data, err := os.ReadFile(filepath.Join("data", "bundled", "manifest.json"))
	if err != nil {
		return SourceManifest{}, err
	}
	var manifest SourceManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return SourceManifest{}, err
	}
	return manifest, nil
}

func validateManifestSources(manifest SourceManifest) []ManifestSourceReport {
	reports := make([]ManifestSourceReport, 0, len(manifest.Sources))
	for _, source := range manifest.Sources {
		report := ManifestSourceReport{
			ID:       source.ID,
			Kind:     source.Kind,
			Workload: source.Workload,
			File:     source.File,
			Status:   "ok",
		}
		if source.Kind == "generated" {
			reports = append(reports, report)
			continue
		}
		if source.File == "" {
			report.Status = "error"
			report.Error = "missing file"
			reports = append(reports, report)
			continue
		}
		records, err := readCorpusRecords(source.File)
		if err != nil {
			report.Status = "error"
			report.Error = err.Error()
			reports = append(reports, report)
			continue
		}
		mix := sourceMix(records, source.ID)
		if mix[source.ID] == 0 {
			report.Status = "error"
			report.Error = "source id not present in file"
		}
		reports = append(reports, report)
	}
	return reports
}

func requiresBundledTrace(workload Workload) bool {
	return workload.ID != "cache-control"
}

func requiresBundledPrompts(workload Workload) bool {
	switch workload.ID {
	case "prod-cache", "cache-control":
		return false
	default:
		return true
	}
}

func corpusInfo(workload Workload) CorpusInfo {
	info := CorpusInfo{}
	info.Requests = workload.DefaultRequests
	tracePath := filepath.Join(bundledTraceDir, workload.ID+".jsonl")
	traceRecords, err := readCorpusRecords(tracePath)
	if err == nil && len(traceRecords) > 0 {
		info.TraceFile = tracePath
		info.TraceRecords = len(traceRecords)
		info.TraceSources = sourceMix(traceRecords, traceDefaultSource(workload.ID))
		if workload.Phase.TraceMode == "source_window" {
			info.Requests = len(traceRecords)
			if workload.DefaultRequests > 0 && workload.DefaultRequests < info.Requests {
				info.Requests = workload.DefaultRequests
			}
		}
	}

	promptPath := filepath.Join(bundledPromptDir, workload.ID+".jsonl")
	promptRecords, err := readCorpusRecords(promptPath)
	if err == nil && len(promptRecords) > 0 {
		info.PromptFile = promptPath
		info.PromptRecords = len(promptRecords)
		info.PromptSources = sourceMix(promptRecords, sourceForCategory(workload.Category))
	}

	if info.TraceFile != "" && info.PromptFile != "" {
		info.Mode = "bundled_trace_and_prompt_corpus"
		return info
	}
	if info.TraceFile != "" {
		info.Mode = "bundled_trace_corpus"
		return info
	}
	if info.PromptFile != "" {
		info.Mode = "bundled_prompt_corpus"
		return info
	}

	switch workload.ID {
	case "prod-cache":
		return CorpusInfo{
			Mode:  "deterministic_generated_trace",
			Notes: "Uses generated cache-block hashes to preserve exact prefix reuse behavior.",
		}
	case "cache-control":
		return CorpusInfo{
			Mode:  "deterministic_generated_control",
			Notes: "Uses generated high-cache and low-cache phases as a calibration workload.",
		}
	default:
		return CorpusInfo{
			Mode:  "deterministic_generated_fixture",
			Notes: "No bundled prompt corpus found; materialization falls back to generated fixtures.",
		}
	}
}

func readCorpusRecords(path string) ([]map[string]any, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readJSONRecords(file, 0)
}

func sourceMix(records []map[string]any, defaultSource string) map[string]int {
	mix := map[string]int{}
	for _, record := range records {
		source := firstString(record, "_source", "source_type")
		if source == "" {
			source = defaultSource
		}
		mix[source]++
	}
	return mix
}

func traceDefaultSource(workloadID string) string {
	switch workloadID {
	case "prod-chat":
		return "burstgpt"
	case "prod-cache":
		return "qwen-bailian"
	case "code-repo":
		return "azure"
	case "rag-cache":
		return "ragpulse"
	case "long-context":
		return "synthetic-long-context"
	case "agentic-tools":
		return "taubench"
	default:
		return "synthetic"
	}
}

func sourceForCategory(category string) string {
	switch category {
	case "production-chat":
		return "sharegpt"
	case "code", "code-repo":
		return "swebench"
	case "long-context":
		return "longbench"
	case "cache", "rag", "agentic", "stateful-cache-agent":
		return "ragpulse"
	default:
		return "generic"
	}
}

func FormatCorpusReport(report CorpusReport) string {
	var b strings.Builder
	if len(report.Manifest) > 0 {
		fmt.Fprintf(&b, "manifest:\n")
		for _, source := range report.Manifest {
			if source.Error != "" {
				fmt.Fprintf(&b, "- %s/%s/%s: error: %s\n", source.Workload, source.Kind, source.ID, source.Error)
			} else {
				fmt.Fprintf(&b, "- %s/%s/%s: %s\n", source.Workload, source.Kind, source.ID, source.Status)
			}
		}
		fmt.Fprintf(&b, "\n")
	}
	for _, workload := range report.Workloads {
		status := "ok"
		if workload.Error != "" {
			status = "error: " + workload.Error
		}
		fmt.Fprintf(&b, "%s: %s (%s)\n", workload.ID, status, workload.Mode)
		if workload.PromptFile != "" {
			fmt.Fprintf(&b, "  file: %s\n", workload.PromptFile)
			fmt.Fprintf(&b, "  records: %d, rendered: %d\n", workload.PromptRecords, workload.RenderedPrompts)
		}
		if workload.TraceFile != "" {
			fmt.Fprintf(&b, "  trace file: %s\n", workload.TraceFile)
			fmt.Fprintf(&b, "  trace records: %d, materialized requests: %d\n", workload.TraceRecords, workload.Requests)
		}
		if len(workload.TraceSources) > 0 {
			fmt.Fprintf(&b, "  trace sources: %s\n", formatSourceMix(workload.TraceSources))
		}
		if len(workload.PromptSources) > 0 {
			fmt.Fprintf(&b, "  prompt sources: %s\n", formatSourceMix(workload.PromptSources))
		}
		if workload.Notes != "" {
			fmt.Fprintf(&b, "  notes: %s\n", workload.Notes)
		}
	}
	return b.String()
}

func CorpusReportHasErrors(report CorpusReport) bool {
	for _, source := range report.Manifest {
		if source.Error != "" {
			return true
		}
	}
	for _, workload := range report.Workloads {
		if workload.Error != "" {
			return true
		}
	}
	return false
}

func formatSourceMix(mix map[string]int) string {
	keys := make([]string, 0, len(mix))
	for key := range mix {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, mix[key]))
	}
	return strings.Join(parts, ", ")
}
