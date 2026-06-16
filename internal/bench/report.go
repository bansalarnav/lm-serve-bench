package bench

import (
	"fmt"
	"io"
	"strings"
)

func PrintSuite(w io.Writer, suite Suite) {
	fmt.Fprintf(w, "%s: %s\n", suite.Name, suite.Description)
	fmt.Fprintf(w, "duration: %d minutes\n", suite.DurationMinutes)
	fmt.Fprintf(w, "primary harness: %s\n", suite.Harness.Primary)
	if suite.Harness.Reason != "" {
		fmt.Fprintf(w, "harness note: %s\n", suite.Harness.Reason)
	}

	fmt.Fprintln(w, "\nworkloads:")
	for _, workload := range suite.Workloads {
		fmt.Fprintf(w, "- %s (%s): %s\n", workload.ID, workload.Category, workload.Description)
		fmt.Fprintf(w, "  pairing: %s\n", workload.Pairing)
		fmt.Fprintf(w, "  traffic: %s\n", sourceNames(workload.TrafficSources))
		fmt.Fprintf(w, "  prompts: %s\n", sourceNames(workload.PromptSources))
		fmt.Fprintf(w, "  metrics: %s\n", strings.Join(workload.MetricsFocus, ", "))
		fmt.Fprintf(w, "  cap: %d max output tokens, %d default requests\n", workload.MaxOutputTokens, workload.DefaultRequests)
	}

	if len(suite.Sweeps) > 0 {
		fmt.Fprintln(w, "\nsweeps:")
		for _, sweep := range suite.Sweeps {
			fmt.Fprintf(w, "- %s: %s\n", sweep.ID, sweep.Description)
		}
	}
}

func sourceNames(sources []DataSource) string {
	if len(sources) == 0 {
		return "none"
	}

	names := make([]string, 0, len(sources))
	for _, source := range sources {
		names = append(names, source.Name)
	}
	return strings.Join(names, ", ")
}
