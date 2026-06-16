package bench

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

type ConvertOptions struct {
	Source string
	Input  string
	Output string
	Limit  int
}

func ConvertTrace(opts ConvertOptions) error {
	in, err := os.Open(opts.Input)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(opts.Output)
	if err != nil {
		return err
	}
	defer out.Close()

	switch strings.ToLower(opts.Source) {
	case "bailian", "qwen":
		return convertBailian(in, out, opts.Limit)
	case "burstgpt":
		return convertBurstGPT(in, out, opts.Limit)
	case "azure":
		return convertAzure(in, out, opts.Limit)
	case "sharegpt", "wildchat", "blazedit", "swebench", "swe-bench", "bigcodebench", "longbench", "infinitebench", "ruler", "taubench", "tau-bench", "tau2", "tau2-bench", "ragpulse":
		return convertPromptDataset(in, out, strings.ToLower(opts.Source), opts.Limit)
	default:
		return fmt.Errorf("unknown source %q; expected bailian, burstgpt, azure, sharegpt, wildchat, blazedit, swebench, bigcodebench, longbench, infinitebench, ruler, taubench, tau2, or ragpulse", opts.Source)
	}
}

func convertPromptDataset(in io.Reader, out io.Writer, source string, limit int) error {
	records, err := readJSONRecords(in, limit)
	if err != nil {
		return err
	}

	writer := bufio.NewWriter(out)
	defer writer.Flush()

	for i, record := range records {
		text := renderPromptRecord(source, record)
		if strings.TrimSpace(text) == "" {
			continue
		}
		entry := AIPerfTraceEntry{
			Timestamp: i * 1000,
			InputText: text,
		}
		if cacheLikePromptSource(source) {
			entry.HashIDs = syntheticHashIDs(i, "stateful-cache-agent", approximateTokenCount(text))
		}
		if err := writeJSONLine(writer, entry); err != nil {
			return err
		}
	}
	return nil
}

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

func cacheLikePromptSource(source string) bool {
	switch source {
	case "ragpulse", "taubench", "tau-bench", "tau2", "tau2-bench":
		return true
	default:
		return false
	}
}

func approximateTokenCount(text string) int {
	count := len(strings.Fields(text))
	if count <= 0 {
		return 1
	}
	return count
}

func convertBailian(in io.Reader, out io.Writer, limit int) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 1024), 16*1024*1024)
	writer := bufio.NewWriter(out)
	defer writer.Flush()

	var firstTimestamp *int64
	count := 0
	for scanner.Scan() {
		if limit > 0 && count >= limit {
			break
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			return fmt.Errorf("line %d: %w", count+1, err)
		}

		timestamp, err := parseTimestampMillis(raw["timestamp"])
		if err != nil {
			return fmt.Errorf("line %d timestamp: %w", count+1, err)
		}
		if firstTimestamp == nil {
			firstTimestamp = &timestamp
		}

		entry := AIPerfTraceEntry{
			Timestamp:   int(timestamp - *firstTimestamp),
			InputLength: intFromAny(raw["input_length"]),
			HashIDs:     intSliceFromAny(raw["hash_ids"]),
		}
		if err := writeJSONLine(writer, entry); err != nil {
			return err
		}
		count++
	}
	return scanner.Err()
}

func convertBurstGPT(in io.Reader, out io.Writer, limit int) error {
	reader := csv.NewReader(in)
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return err
	}
	columns := csvColumns(header)

	writer := bufio.NewWriter(out)
	defer writer.Flush()

	var firstTimestamp *int64
	count := 0
	for {
		if limit > 0 && count >= limit {
			break
		}
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		timestamp, err := parseTimestampMillis(recordValue(record, columns, "timestamp"))
		if err != nil {
			return fmt.Errorf("row %d timestamp: %w", count+2, err)
		}
		if firstTimestamp == nil {
			firstTimestamp = &timestamp
		}

		entry := AIPerfTraceEntry{
			Timestamp:   int(timestamp - *firstTimestamp),
			InputLength: intFromString(recordValue(record, columns, "request tokens")),
		}
		if err := writeJSONLine(writer, entry); err != nil {
			return err
		}
		count++
	}
	return nil
}

func convertAzure(in io.Reader, out io.Writer, limit int) error {
	reader := csv.NewReader(in)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	header, err := reader.Read()
	if err != nil {
		return err
	}
	if len(header) == 1 && strings.Contains(header[0], "\t") {
		reader.Comma = '\t'
		header = strings.Split(header[0], "\t")
	}
	columns := csvColumns(header)

	writer := bufio.NewWriter(out)
	defer writer.Flush()

	var firstTimestamp *int64
	count := 0
	for {
		if limit > 0 && count >= limit {
			break
		}
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		timestamp, err := parseTimestampMillis(recordValue(record, columns, "timestamp"))
		if err != nil {
			return fmt.Errorf("row %d timestamp: %w", count+2, err)
		}
		if firstTimestamp == nil {
			firstTimestamp = &timestamp
		}

		entry := AIPerfTraceEntry{
			Timestamp:   int(timestamp - *firstTimestamp),
			InputLength: intFromString(firstNonEmpty(recordValue(record, columns, "contexttokens"), recordValue(record, columns, "context tokens"))),
		}
		if err := writeJSONLine(writer, entry); err != nil {
			return err
		}
		count++
	}
	return nil
}

func writeJSONLine(w *bufio.Writer, entry AIPerfTraceEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

func csvColumns(header []string) map[string]int {
	columns := make(map[string]int, len(header))
	for index, name := range header {
		columns[normalizeColumn(name)] = index
	}
	return columns
}

func recordValue(record []string, columns map[string]int, name string) string {
	index, ok := columns[normalizeColumn(name)]
	if !ok || index >= len(record) {
		return ""
	}
	return strings.TrimSpace(record[index])
}

func normalizeColumn(name string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), "_", " "))
}

func parseTimestampMillis(value any) (int64, error) {
	switch typed := value.(type) {
	case nil:
		return 0, fmt.Errorf("missing timestamp")
	case string:
		return parseTimestampString(typed)
	case float64:
		return numericTimestampMillis(typed), nil
	case int:
		return numericTimestampMillis(float64(typed)), nil
	case int64:
		return numericTimestampMillis(float64(typed)), nil
	default:
		return 0, fmt.Errorf("unsupported %T", value)
	}
}

func parseTimestampString(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("missing timestamp")
	}
	if number, err := strconv.ParseFloat(value, 64); err == nil {
		return numericTimestampMillis(number), nil
	}

	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05",
		"2006/01/02 15:04:05",
	}
	for _, format := range formats {
		parsed, err := time.Parse(format, value)
		if err == nil {
			return parsed.UnixMilli(), nil
		}
	}
	return 0, fmt.Errorf("unsupported timestamp %q", value)
}

func numericTimestampMillis(value float64) int64 {
	if value > 1_000_000_000_000 {
		return int64(value)
	}
	if value > 1_000_000_000 {
		return int64(value * 1000)
	}
	return int64(value * 1000)
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case string:
		return intFromString(typed)
	default:
		return 0
	}
}

func intFromString(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return int(number)
}

func intSliceFromAny(value any) []int {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}

	ints := make([]int, 0, len(raw))
	for _, item := range raw {
		next := intFromAny(item)
		if next != 0 {
			ints = append(ints, next)
		}
	}
	return ints
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
