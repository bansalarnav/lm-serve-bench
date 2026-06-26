package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/arnav/lm-serve-bench/internal/bench"
)

const defaultSuitePath = "data/suites/default.json"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "run":
		runBenchmark(os.Args[2:])
	case "mock-server":
		runMockServer(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

type chatRequest struct {
	Model               string        `json:"model"`
	Messages            []chatMessage `json:"messages"`
	MaxTokens           int           `json:"max_tokens"`
	MaxCompletionTokens int           `json:"max_completion_tokens"`
	MaxNewTokens        int           `json:"max_new_tokens"`
	Stream              bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func runMockServer(args []string) {
	fs := flag.NewFlagSet("mock-server", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8000", "listen address")
	tokenDelay := fs.Duration("token-delay", 2*time.Millisecond, "delay between streamed tokens")
	_ = fs.Parse(args)

	log.Printf("mock OpenAI-compatible endpoint listening on http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mockMux(*tokenDelay)))
}

func mockMux(tokenDelay time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Model == "" {
			req.Model = "mock-model"
		}
		if req.MaxTokens <= 0 {
			req.MaxTokens = firstPositive(req.MaxCompletionTokens, req.MaxNewTokens, 32)
		}

		if req.Stream {
			writeStreamingChat(w, req, tokenDelay)
			return
		}
		writeChat(w, req)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}

func writeChat(w http.ResponseWriter, req chatRequest) {
	content := mockCompletion(req)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":      "chatcmpl-mock",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]string{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]int{
			"prompt_tokens":     approximatePromptTokens(req.Messages),
			"completion_tokens": len(strings.Fields(content)),
			"total_tokens":      approximatePromptTokens(req.Messages) + len(strings.Fields(content)),
		},
	})
}

func writeStreamingChat(w http.ResponseWriter, req chatRequest, tokenDelay time.Duration) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	for _, token := range strings.Fields(mockCompletion(req)) {
		writeSSE(w, req.Model, map[string]string{"content": token + " "}, nil)
		flusher.Flush()
		time.Sleep(tokenDelay)
	}
	finish := "stop"
	writeSSE(w, req.Model, map[string]string{}, &finish)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func writeSSE(w http.ResponseWriter, model string, delta map[string]string, finishReason *string) {
	payload := map[string]any{
		"id":      "chatcmpl-mock",
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         0,
				"delta":         delta,
				"finish_reason": finishReason,
			},
		},
	}
	data, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
}

func mockCompletion(req chatRequest) string {
	words := []string{"ok"}
	if len(req.Messages) > 0 {
		last := req.Messages[len(req.Messages)-1].Content
		if last != "" {
			words = append(words, "processed", fmt.Sprintf("%d", len(strings.Fields(last))), "input", "words")
		}
	}
	for len(words) < req.MaxTokens {
		words = append(words, "token")
	}
	if len(words) > req.MaxTokens {
		words = words[:req.MaxTokens]
	}
	return strings.Join(words, " ")
}

func approximatePromptTokens(messages []chatMessage) int {
	total := 0
	for _, message := range messages {
		total += len(strings.Fields(message.Content))
	}
	return total
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func runBenchmark(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	suitePath := fs.String("suite", defaultSuitePath, "suite JSON file")
	outDir := fs.String("out", "out/default", "prepared workload directory")
	workloadID := fs.String("workload", "all", "workload id to run, or all for mixed replay")
	mooncakeTrace := fs.String("mooncake-trace", "", "run this Mooncake-format trace JSONL instead of the bundled benchmark workloads")
	model := fs.String("model", "Qwen/Qwen3-0.6B", "model name passed to AIPerf")
	endpoint := fs.String("endpoint", "http://localhost:8000", "OpenAI-compatible endpoint base URL")
	duration := fs.Duration("duration", 0, "override fixed trace replay duration per workload, for example 10m or 90s")
	sweep := fs.String("sweep", "", "optional sweep to run: load, throughput, or rps")
	targetRPS := fs.Float64("target-rps", 0, "target requests per second for --sweep rps")
	targetQPS := fs.Float64("target-qps", 0, "alias for --target-rps")
	rampSteps := fs.Int("ramp-steps", 5, "number of ramp stages for --sweep rps")
	dryRun := fs.Bool("dry-run", false, "print AIPerf commands without executing them")
	_ = fs.Parse(args)

	if *targetRPS <= 0 && *targetQPS > 0 {
		*targetRPS = *targetQPS
	}
	if *duration > 0 && *sweep != "" {
		fail(fmt.Errorf("--duration only applies to default trace replay; use load, rps, or throughput sweep knobs for --sweep %s", *sweep))
	}
	suite, err := bench.LoadSuite(*suitePath)
	if err != nil {
		fail(err)
	}
	if err := executeSuite(executeOptions{
		Suite:         suite,
		OutDir:        *outDir,
		WorkloadID:    *workloadID,
		MooncakeTrace: *mooncakeTrace,
		Model:         *model,
		Endpoint:      *endpoint,
		Duration:      *duration,
		Sweep:         *sweep,
		TargetRPS:     *targetRPS,
		RampSteps:     *rampSteps,
		DryRun:        *dryRun,
	}); err != nil {
		fail(err)
	}
}

type executeOptions struct {
	Suite         bench.Suite
	OutDir        string
	WorkloadID    string
	MooncakeTrace string
	Model         string
	Endpoint      string
	Duration      time.Duration
	Sweep         string
	TargetRPS     float64
	RampSteps     int
	DryRun        bool
}

func executeSuite(opts executeOptions) error {
	if opts.WorkloadID == "" {
		opts.WorkloadID = "all"
	}
	if opts.Model == "" {
		opts.Model = "Qwen/Qwen3-0.6B"
	}
	if opts.Endpoint == "" {
		opts.Endpoint = "http://localhost:8000"
	}
	if opts.MooncakeTrace != "" {
		return executeMooncakeTrace(opts)
	}
	if err := bench.MaterializeSuite(opts.Suite, opts.OutDir); err != nil {
		return err
	}
	if opts.WorkloadID == "all" && opts.Sweep == "" {
		durationSeconds := opts.Suite.DurationMinutes * 60
		if opts.Duration > 0 {
			durationSeconds = int(opts.Duration.Seconds())
		}
		mixed, err := bench.MaterializeMixedReplay(opts.Suite, opts.OutDir, durationSeconds)
		if err != nil {
			return err
		}
		workloadDir := filepath.Join(opts.OutDir, mixed.ID)
		scriptName := "run.sh"
		script := filepath.Join(workloadDir, scriptName)
		fmt.Fprintf(os.Stdout, "workload %s\n", mixed.ID)
		fmt.Fprintf(os.Stdout, "  %s\n", script)
		if opts.DryRun {
			return nil
		}

		cmd := exec.Command("sh", scriptName)
		cmd.Dir = workloadDir
		cmd.Env = append(os.Environ(),
			"MODEL_NAME="+opts.Model,
			"INFERENCE_URL="+opts.Endpoint,
			"ARTIFACT_DIR="+filepath.Join("artifacts", mixed.ID),
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s failed: %w", mixed.ID, err)
		}

		summarySuite := opts.Suite
		summarySuite.Workloads = []bench.Workload{mixed}
		summary, err := bench.SummarizeResults(summarySuite, opts.OutDir)
		if err != nil {
			return err
		}
		if err := bench.WriteSummaryFiles(summary, opts.OutDir); err != nil {
			return err
		}
		fmt.Fprint(os.Stdout, bench.PrintTerminalSummary(summary))
		return nil
	}

	runs := 0
	for _, workload := range opts.Suite.Workloads {
		if opts.WorkloadID != "all" && workload.ID != opts.WorkloadID {
			continue
		}
		runs++
		workloadDir := filepath.Join(opts.OutDir, workload.ID)
		if opts.Sweep == "" && opts.Duration > 0 {
			if err := bench.ScaleWorkloadTraceDuration(workloadDir, int(opts.Duration.Seconds())); err != nil {
				return err
			}
		}
		if opts.Sweep == "rps" {
			if err := bench.MaterializeRPSSweep(workloadDir, workload, opts.TargetRPS, opts.RampSteps); err != nil {
				return err
			}
		}
		scriptName, err := scriptForSweep(opts.Sweep)
		if err != nil {
			return err
		}
		script := filepath.Join(workloadDir, scriptName)
		fmt.Fprintf(os.Stdout, "workload %s\n", workload.ID)
		fmt.Fprintf(os.Stdout, "  %s\n", script)
		if opts.DryRun {
			continue
		}

		cmd := exec.Command("sh", scriptName)
		cmd.Dir = workloadDir
		env := append(os.Environ(),
			"MODEL_NAME="+opts.Model,
			"INFERENCE_URL="+opts.Endpoint,
		)
		if opts.Sweep == "" {
			env = append(env, "ARTIFACT_DIR="+filepath.Join("artifacts", workload.ID))
		}
		cmd.Env = env
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s failed: %w", workload.ID, err)
		}
	}
	if runs == 0 {
		return fmt.Errorf("workload %q was not found", opts.WorkloadID)
	}
	if !opts.DryRun {
		summary, err := bench.SummarizeResults(opts.Suite, opts.OutDir)
		if err != nil {
			return err
		}
		if err := bench.WriteSummaryFiles(summary, opts.OutDir); err != nil {
			return err
		}
		fmt.Fprint(os.Stdout, bench.PrintTerminalSummary(summary))
	}
	return nil
}

func executeMooncakeTrace(opts executeOptions) error {
	if opts.WorkloadID != "" && opts.WorkloadID != "all" {
		return fmt.Errorf("--mooncake-trace runs as custom-trace; do not combine it with --workload")
	}
	if opts.Sweep == "throughput" {
		return fmt.Errorf("--mooncake-trace cannot be combined with --sweep throughput because throughput mode uses a random prompt pool, not trace replay")
	}

	workload, err := bench.MaterializeMooncakeTrace(opts.OutDir, opts.MooncakeTrace, opts.Suite.Sweeps)
	if err != nil {
		return err
	}
	workloadDir := filepath.Join(opts.OutDir, workload.ID)
	if opts.Sweep == "" && opts.Duration > 0 {
		if err := bench.ScaleWorkloadTraceDuration(workloadDir, int(opts.Duration.Seconds())); err != nil {
			return err
		}
	}
	if opts.Sweep == "rps" {
		if err := bench.MaterializeRPSSweep(workloadDir, workload, opts.TargetRPS, opts.RampSteps); err != nil {
			return err
		}
	}
	scriptName, err := scriptForSweep(opts.Sweep)
	if err != nil {
		return err
	}
	script := filepath.Join(workloadDir, scriptName)
	fmt.Fprintf(os.Stdout, "workload %s\n", workload.ID)
	fmt.Fprintf(os.Stdout, "  %s\n", script)
	if opts.DryRun {
		return nil
	}

	cmd := exec.Command("sh", scriptName)
	cmd.Dir = workloadDir
	cmd.Env = append(os.Environ(),
		"MODEL_NAME="+opts.Model,
		"INFERENCE_URL="+opts.Endpoint,
		"ARTIFACT_DIR="+filepath.Join("artifacts", workload.ID),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", workload.ID, err)
	}

	summarySuite := opts.Suite
	summarySuite.Workloads = []bench.Workload{workload}
	summary, err := bench.SummarizeResults(summarySuite, opts.OutDir)
	if err != nil {
		return err
	}
	if err := bench.WriteSummaryFiles(summary, opts.OutDir); err != nil {
		return err
	}
	fmt.Fprint(os.Stdout, bench.PrintTerminalSummary(summary))
	return nil
}

func scriptForSweep(sweep string) (string, error) {
	switch sweep {
	case "":
		return "run.sh", nil
	case "load":
		return "sweep-load.sh", nil
	case "throughput":
		return "sweep-throughput.sh", nil
	case "rps":
		return "sweep-rps.sh", nil
	default:
		return "", fmt.Errorf("unknown sweep %q; expected load, throughput, or rps", sweep)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  lm-serve-bench run [--suite data/suites/default.json] [--out out/default] [--workload all] [--mooncake-trace trace.jsonl] [--model Qwen/Qwen3-0.6B] [--endpoint http://localhost:8000] [--duration 25m] [--sweep load|throughput|rps] [--target-rps 100] [--ramp-steps 5] [--dry-run]
  lm-serve-bench mock-server [--addr 127.0.0.1:8000] [--token-delay 2ms]`)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
