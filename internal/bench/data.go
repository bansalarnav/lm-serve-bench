package bench

import (
	"os"
	"path/filepath"
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
