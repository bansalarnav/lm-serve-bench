package bench

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
)

func readPromptTexts(path string, category string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	source := category
	switch category {
	case "production-chat":
		source = "sharegpt"
	case "code", "code-repo":
		source = "swebench"
	case "long-context":
		source = "longbench"
	case "cache", "rag", "agentic", "stateful-cache-agent":
		source = "ragpulse"
	}

	records, err := readJSONRecords(file, 0)
	if err != nil {
		return nil, err
	}
	prompts := make([]string, 0, len(records))
	for _, record := range records {
		recordSource := firstString(record, "_source", "source_type")
		if recordSource == "" {
			recordSource = source
		}
		if text := strings.TrimSpace(renderPromptRecord(recordSource, record)); text != "" {
			prompts = append(prompts, text)
		}
	}
	return prompts, nil
}

func readJSONRecords(in io.Reader, limit int) ([]map[string]any, error) {
	data, err := io.ReadAll(in)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	if data[0] == '[' {
		var records []map[string]any
		if err := json.Unmarshal(data, &records); err != nil {
			return nil, err
		}
		if limit > 0 && len(records) > limit {
			return records[:limit], nil
		}
		return records, nil
	}

	var records []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024), 16*1024*1024)
	for scanner.Scan() {
		if limit > 0 && len(records) >= limit {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, fmt.Errorf("line %d: %w", len(records)+1, err)
		}
		records = append(records, record)
	}
	return records, scanner.Err()
}

func renderPromptRecord(source string, record map[string]any) string {
	switch source {
	case "sharegpt":
		return renderChatRecord(record, []string{"conversations", "messages"})
	case "wildchat":
		return renderChatRecord(record, []string{"conversation", "messages", "turns"})
	case "swebench", "swe-bench":
		return renderSWEBenchRecord(record)
	case "bigcodebench":
		return renderBigCodeBenchRecord(record)
	case "blazedit":
		return renderBlazeditRecord(record)
	case "longbench", "infinitebench", "ruler":
		return renderLongContextRecord(record)
	case "taubench", "tau-bench", "tau2", "tau2-bench":
		return renderTauRecord(record)
	case "ragpulse":
		return renderRAGRecord(record)
	default:
		return renderGenericRecord(record)
	}
}

func renderChatRecord(record map[string]any, keys []string) string {
	for _, key := range keys {
		if turns, ok := record[key].([]any); ok {
			var parts []string
			for _, turn := range turns {
				item, ok := turn.(map[string]any)
				if !ok {
					continue
				}
				role := firstString(item, "role", "from", "speaker")
				content := firstString(item, "content", "value", "text")
				if content == "" {
					continue
				}
				if role == "" {
					role = "message"
				}
				parts = append(parts, role+": "+content)
			}
			if len(parts) > 0 {
				return strings.Join(parts, "\n\n")
			}
		}
	}
	return renderGenericRecord(record)
}

func renderSWEBenchRecord(record map[string]any) string {
	repo := firstString(record, "repo")
	instance := firstString(record, "instance_id")
	problem := firstString(record, "problem_statement")
	hints := firstString(record, "hints_text")
	base := firstString(record, "base_commit")

	var b strings.Builder
	if repo != "" {
		fmt.Fprintf(&b, "Repository: %s\n", repo)
	}
	if instance != "" {
		fmt.Fprintf(&b, "Instance: %s\n", instance)
	}
	if base != "" {
		fmt.Fprintf(&b, "Base commit: %s\n", base)
	}
	if problem != "" {
		fmt.Fprintf(&b, "\nIssue:\n%s\n", problem)
	}
	if hints != "" {
		fmt.Fprintf(&b, "\nHints:\n%s\n", hints)
	}
	b.WriteString("\nGenerate a minimal patch that fixes the issue. Return only the patch or the edited code needed.")
	return b.String()
}

func renderBigCodeBenchRecord(record map[string]any) string {
	prompt := firstString(record, "instruct_prompt", "complete_prompt", "prompt", "canonical_prompt", "description")
	taskID := firstString(record, "task_id", "id")
	var b strings.Builder
	if taskID != "" {
		fmt.Fprintf(&b, "Task: %s\n\n", taskID)
	}
	b.WriteString(prompt)
	if libs := firstString(record, "libs", "required_dependencies"); libs != "" {
		fmt.Fprintf(&b, "\n\nAvailable libraries:\n%s", libs)
	}
	return b.String()
}

func renderBlazeditRecord(record map[string]any) string {
	instruction := firstString(record, "instruction", "prompt", "edit_instruction")
	before := firstString(record, "input", "source", "before", "old_code", "code")
	var b strings.Builder
	if instruction != "" {
		fmt.Fprintf(&b, "Edit instruction:\n%s\n", instruction)
	}
	if before != "" {
		fmt.Fprintf(&b, "\nCode/context:\n%s\n", before)
	}
	b.WriteString("\nReturn the edited code.")
	return b.String()
}

func renderLongContextRecord(record map[string]any) string {
	context := firstString(record, "context", "document", "documents", "passage", "repo")
	question := firstString(record, "question", "input", "query", "prompt")
	var b strings.Builder
	if context != "" {
		fmt.Fprintf(&b, "Context:\n%s\n", context)
	}
	if question != "" {
		fmt.Fprintf(&b, "\nQuestion:\n%s\n", question)
	}
	for _, choice := range []string{"choice_A", "choice_B", "choice_C", "choice_D"} {
		if value := firstString(record, choice); value != "" {
			fmt.Fprintf(&b, "%s: %s\n", choice, value)
		}
	}
	b.WriteString("\nAnswer using only the provided context.")
	return b.String()
}

func renderTauRecord(record map[string]any) string {
	task := firstString(record, "instruction", "task", "user_prompt", "goal", "description")
	if task == "" {
		task = renderChatRecord(record, []string{"messages", "trajectory", "turns"})
	}
	tools := anyToText(record["tools"])
	state := anyToText(firstPresent(record, "initial_state", "database", "state"))
	var b strings.Builder
	if task != "" {
		fmt.Fprintf(&b, "Agent task:\n%s\n", task)
	}
	if tools != "" {
		fmt.Fprintf(&b, "\nAvailable tools:\n%s\n", tools)
	}
	if state != "" {
		fmt.Fprintf(&b, "\nState:\n%s\n", state)
	}
	b.WriteString("\nDecide the next assistant response or tool call.")
	return b.String()
}

func renderRAGRecord(record map[string]any) string {
	system := firstString(record, "system", "system_prompt")
	docs := firstString(record, "documents", "retrieved_docs", "context", "passages")
	history := firstString(record, "history", "chat_history", "messages")
	question := firstString(record, "question", "query", "user_input", "input")
	var b strings.Builder
	if system != "" {
		fmt.Fprintf(&b, "System:\n%s\n", system)
	}
	if docs != "" {
		fmt.Fprintf(&b, "\nRetrieved context:\n%s\n", docs)
	}
	if history != "" {
		fmt.Fprintf(&b, "\nConversation history:\n%s\n", history)
	}
	if question != "" {
		fmt.Fprintf(&b, "\nUser question:\n%s\n", question)
	}
	b.WriteString("\nAnswer based on the retrieved context.")
	return b.String()
}

func renderGenericRecord(record map[string]any) string {
	if text := firstString(record, "input_text", "text", "prompt", "input", "question", "query", "content"); text != "" {
		return text
	}
	data, _ := json.Marshal(record)
	return string(data)
}

func firstString(record map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := anyToText(firstPresent(record, key)); strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstPresent(record map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := record[key]; ok {
			return value
		}
	}
	return nil
}

func anyToText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []any, map[string]any:
		data, _ := json.Marshal(typed)
		return string(data)
	default:
		return fmt.Sprint(typed)
	}
}

func approximateTokenCount(text string) int {
	count := len(strings.Fields(text))
	if count <= 0 {
		return 1
	}
	return count
}

func selectTraceWindow(entries []AIPerfTraceEntry, windowMillis int, selection string) []AIPerfTraceEntry {
	if len(entries) == 0 || windowMillis <= 0 {
		return entries
	}
	selection = strings.TrimSpace(strings.ToLower(selection))
	if selection == "" {
		selection = "first"
	}

	bestStart := 0
	bestEnd := 0
	bestScore := math.Inf(-1)

	end := 0
	var lengthSum, lengthSquares float64
	var gapSum, gapSquares float64
	gapCount := 0
	for start := range entries {
		for end < len(entries) && entries[end].Timestamp <= entries[start].Timestamp+windowMillis {
			length := float64(entries[end].InputLength)
			lengthSum += length
			lengthSquares += length * length
			if end > start {
				gap := float64(entries[end].Timestamp - entries[end-1].Timestamp)
				if gap < 0 {
					gap = 0
				}
				gapSum += gap
				gapSquares += gap * gap
				gapCount++
			}
			end++
		}

		count := end - start
		if count <= 0 {
			continue
		}
		score := float64(count)
		if selection == "high-variance" || selection == "variance" {
			score = traceWindowScore(count, lengthSum, lengthSquares, gapCount, gapSum, gapSquares)
		}
		if score > bestScore {
			bestScore = score
			bestStart = start
			bestEnd = end
		}
		if selection == "first" {
			break
		}

		length := float64(entries[start].InputLength)
		lengthSum -= length
		lengthSquares -= length * length
		if start+1 < end {
			gap := float64(entries[start+1].Timestamp - entries[start].Timestamp)
			if gap < 0 {
				gap = 0
			}
			gapSum -= gap
			gapSquares -= gap * gap
			gapCount--
		}
	}

	selected := append([]AIPerfTraceEntry(nil), entries[bestStart:bestEnd]...)
	normalizeTraceTimestamps(selected)
	return selected
}

func traceWindowScore(count int, lengthSum, lengthSquares float64, gapCount int, gapSum, gapSquares float64) float64 {
	if count == 0 {
		return math.Inf(-1)
	}

	mean := lengthSum / float64(count)
	variance := lengthSquares/float64(count) - mean*mean
	if variance < 0 {
		variance = 0
	}

	burstScore := 0.0
	if gapCount > 0 {
		gapMean := gapSum / float64(gapCount)
		gapVariance := gapSquares/float64(gapCount) - gapMean*gapMean
		if gapVariance < 0 {
			gapVariance = 0
		}
		burstScore = math.Sqrt(gapVariance) / math.Max(gapMean, 1)
	}

	return float64(count)*10 + math.Log1p(variance) + burstScore*25
}

func normalizeTraceTimestamps(entries []AIPerfTraceEntry) {
	if len(entries) == 0 {
		return
	}
	first := entries[0].Timestamp
	for i := range entries {
		entries[i].Timestamp -= first
		if entries[i].Timestamp < 0 {
			entries[i].Timestamp = 0
		}
	}
}
