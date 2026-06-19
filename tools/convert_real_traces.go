package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type traceRow struct {
	Source      string `json:"_source,omitempty"`
	Timestamp   int    `json:"timestamp"`
	InputLength int    `json:"input_length,omitempty"`
	HashIDs     []int  `json:"hash_ids,omitempty"`
}

func main() {
	if len(os.Args) < 4 {
		fatalf("usage: go run tools/convert_real_traces.go ragpulse|bailian-long|mooncake-long|tau2 <out.jsonl> <input...>")
	}

	mode := os.Args[1]
	outPath := os.Args[2]
	inputs := os.Args[3:]

	var rows []traceRow
	var err error
	switch mode {
	case "ragpulse":
		rows, err = convertRAGPulse(inputs[0])
	case "bailian-long":
		rows, err = convertBailianLong(inputs[0])
	case "mooncake-long":
		rows, err = convertMooncakeLong(inputs[0])
	case "tau2":
		rows, err = convertTau2(inputs)
	default:
		err = fmt.Errorf("unknown mode %q", mode)
	}
	if err != nil {
		fatalf("%v", err)
	}
	if err := writeRows(outPath, rows); err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("wrote %d rows to %s\n", len(rows), outPath)
}

func convertMooncakeLong(path string) ([]traceRow, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	type mooncakeRow struct {
		Timestamp   int   `json:"timestamp"`
		InputLength int   `json:"input_length"`
		HashIDs     []int `json:"hash_ids"`
	}

	var rows []traceRow
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 16*1024*1024)
	for scanner.Scan() {
		var in mooncakeRow
		if err := json.Unmarshal(scanner.Bytes(), &in); err != nil {
			return nil, err
		}
		if in.InputLength < 16000 {
			continue
		}
		rows = append(rows, traceRow{
			Source:      "mooncake-arxiv",
			Timestamp:   in.Timestamp,
			InputLength: in.InputLength,
			HashIDs:     in.HashIDs,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return selectHighestVarianceWindow(rows, 25*60*1000), nil
}

func convertBailianLong(path string) ([]traceRow, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	type bailianRow struct {
		Timestamp   float64 `json:"timestamp"`
		InputLength int     `json:"input_length"`
		HashIDs     []int   `json:"hash_ids"`
	}

	var rows []traceRow
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 16*1024*1024)
	for scanner.Scan() {
		var in bailianRow
		if err := json.Unmarshal(scanner.Bytes(), &in); err != nil {
			return nil, err
		}
		if in.InputLength < 16000 {
			continue
		}
		rows = append(rows, traceRow{
			Source:      "qwen-bailian-thinking",
			Timestamp:   int(math.Round(in.Timestamp * 1000)),
			InputLength: in.InputLength,
			HashIDs:     in.HashIDs,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return selectHighestVarianceWindow(rows, 25*60*1000), nil
}

type ragPulseRow struct {
	Timestamp   string           `json:"timestamp"`
	InputLength int              `json:"input_length"`
	HashIDs     map[string][]int `json:"hash_ids"`
}

func convertRAGPulse(path string) ([]traceRow, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var rows []traceRow
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var in ragPulseRow
		if err := json.Unmarshal([]byte(line), &in); err != nil {
			return nil, err
		}
		seconds, err := strconv.Atoi(in.Timestamp)
		if err != nil {
			return nil, err
		}
		rows = append(rows, traceRow{
			Source:      "ragpulse",
			Timestamp:   seconds * 1000,
			InputLength: in.InputLength,
			HashIDs:     flattenRAGPulseHashIDs(in.HashIDs),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return selectHighestVarianceWindow(rows, 25*60*1000), nil
}

// selectHighestVarianceWindow keeps one real 25-minute RAGPulse segment. It
// favors busy windows with varied prompt lengths and inter-arrival gaps.
func selectHighestVarianceWindow(rows []traceRow, windowMillis int) []traceRow {
	if len(rows) == 0 || windowMillis <= 0 {
		return rows
	}

	bestStart, bestEnd := 0, 0
	bestScore := math.Inf(-1)
	end := 0
	var lengthSum, lengthSquares, gapSum, gapSquares float64
	gapCount := 0

	for start := range rows {
		for end < len(rows) && rows[end].Timestamp <= rows[start].Timestamp+windowMillis {
			length := float64(rows[end].InputLength)
			lengthSum += length
			lengthSquares += length * length
			if end > start {
				gap := float64(rows[end].Timestamp - rows[end-1].Timestamp)
				gapSum += gap
				gapSquares += gap * gap
				gapCount++
			}
			end++
		}

		count := end - start
		if score := windowScore(count, lengthSum, lengthSquares, gapCount, gapSum, gapSquares); score > bestScore {
			bestScore = score
			bestStart, bestEnd = start, end
		}

		length := float64(rows[start].InputLength)
		lengthSum -= length
		lengthSquares -= length * length
		if start+1 < end {
			gap := float64(rows[start+1].Timestamp - rows[start].Timestamp)
			gapSum -= gap
			gapSquares -= gap * gap
			gapCount--
		}
	}

	selected := append([]traceRow(nil), rows[bestStart:bestEnd]...)
	first := selected[0].Timestamp
	for i := range selected {
		selected[i].Timestamp -= first
	}
	return selected
}

func windowScore(count int, lengthSum, lengthSquares float64, gapCount int, gapSum, gapSquares float64) float64 {
	if count == 0 {
		return math.Inf(-1)
	}
	mean := lengthSum / float64(count)
	lengthVariance := math.Max(0, lengthSquares/float64(count)-mean*mean)

	burstScore := 0.0
	if gapCount > 0 {
		gapMean := gapSum / float64(gapCount)
		gapVariance := math.Max(0, gapSquares/float64(gapCount)-gapMean*gapMean)
		burstScore = math.Sqrt(gapVariance) / math.Max(gapMean, 1)
	}
	return float64(count)*10 + math.Log1p(lengthVariance) + burstScore*25
}

func flattenRAGPulseHashIDs(values map[string][]int) []int {
	keys := []string{"sys_prompt", "passages_ids", "history", "web_search", "user_input"}
	var ids []int
	for _, key := range keys {
		ids = append(ids, values[key]...)
	}
	return ids
}

func convertTau2(paths []string) ([]traceRow, error) {
	var rows []traceRow
	var first time.Time
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		var payload any
		err = json.NewDecoder(file).Decode(&payload)
		_ = file.Close()
		if err != nil && err != io.EOF {
			return nil, err
		}

		source := "tau2:" + strings.TrimSuffix(strings.TrimPrefix(path, "/private/tmp/tau2_"), ".json")
		walkTau2(payload, source, func(row traceRow, ts time.Time) {
			if first.IsZero() || ts.Before(first) {
				first = ts
			}
			rows = append(rows, row)
		})
	}
	if first.IsZero() {
		return nil, fmt.Errorf("no tau2 assistant turns with timestamp and usage found")
	}
	for i := range rows {
		ts := time.UnixMilli(int64(rows[i].Timestamp))
		rows[i].Timestamp = int(ts.Sub(first).Milliseconds())
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].Timestamp < rows[j].Timestamp
	})
	return rows, nil
}

func walkTau2(value any, source string, emit func(traceRow, time.Time)) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			walkTau2(item, source, emit)
		}
	case map[string]any:
		if row, ts, ok := tau2TraceRow(typed, source); ok {
			emit(row, ts)
		}
		for _, item := range typed {
			walkTau2(item, source, emit)
		}
	}
}

func tau2TraceRow(value map[string]any, source string) (traceRow, time.Time, bool) {
	if stringValue(value["role"]) != "assistant" {
		return traceRow{}, time.Time{}, false
	}
	timestampText := stringValue(value["timestamp"])
	if timestampText == "" {
		return traceRow{}, time.Time{}, false
	}
	ts, err := time.Parse("2006-01-02T15:04:05.999999", timestampText)
	if err != nil {
		return traceRow{}, time.Time{}, false
	}
	usage, ok := value["usage"].(map[string]any)
	if !ok {
		return traceRow{}, time.Time{}, false
	}
	promptTokens := intValue(usage["prompt_tokens"])
	if promptTokens <= 0 {
		return traceRow{}, time.Time{}, false
	}

	hashIDs := []int{stableHashID("tau2:source", source)}
	if toolName := toolCallNames(value["tool_calls"]); toolName != "" {
		hashIDs = append(hashIDs, stableHashID("tau2:tool_calls", toolName))
	}
	return traceRow{
		Source:      source,
		Timestamp:   int(ts.UnixMilli()),
		InputLength: promptTokens,
		HashIDs:     hashIDs,
	}, ts, true
}

func toolCallNames(value any) string {
	calls, ok := value.([]any)
	if !ok || len(calls) == 0 {
		return ""
	}
	var names []string
	for _, call := range calls {
		callMap, ok := call.(map[string]any)
		if !ok {
			continue
		}
		if name := stringValue(callMap["name"]); name != "" {
			names = append(names, name)
			continue
		}
		fn, ok := callMap["function"].(map[string]any)
		if !ok {
			continue
		}
		if name := stringValue(fn["name"]); name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func writeRows(path string, rows []traceRow) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	for _, row := range rows {
		data, err := json.Marshal(row)
		if err != nil {
			return err
		}
		if _, err := writer.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func intValue(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	default:
		return 0
	}
}

func stableHashID(namespace string, value string) int {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(namespace))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strings.TrimSpace(value)))
	return int(hash.Sum32()&0x7fffffff) + 1
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
