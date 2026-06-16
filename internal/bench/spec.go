package bench

import (
	"encoding/json"
	"fmt"
	"os"
)

type Suite struct {
	Name            string     `json:"name"`
	Description     string     `json:"description"`
	DurationMinutes int        `json:"duration_minutes"`
	Harness         Harness    `json:"harness"`
	Workloads       []Workload `json:"workloads"`
	Sweeps          []Sweep    `json:"sweeps"`
}

type Harness struct {
	Primary string `json:"primary"`
	Reason  string `json:"reason"`
}

type Workload struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Category        string       `json:"category"`
	Description     string       `json:"description"`
	TrafficSources  []DataSource `json:"traffic_sources"`
	PromptSources   []DataSource `json:"prompt_sources"`
	Pairing         string       `json:"pairing"`
	MetricsFocus    []string     `json:"metrics_focus"`
	MaxOutputTokens int          `json:"max_output_tokens"`
	DefaultRequests int          `json:"default_requests"`
	Phase           Phase        `json:"phase"`
}

type DataSource struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Notes string `json:"notes"`
}

type Sweep struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	Multipliers []float64 `json:"multipliers,omitempty"`
	Concurrency []int     `json:"concurrency,omitempty"`
}

type Phase struct {
	Type            string `json:"type"`
	DurationSeconds int    `json:"duration_seconds"`
	WarmupRequests  int    `json:"warmup_requests"`
}

func LoadSuite(path string) (Suite, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Suite{}, err
	}

	var suite Suite
	if err := json.Unmarshal(data, &suite); err != nil {
		return Suite{}, err
	}
	if suite.Name == "" {
		return Suite{}, fmt.Errorf("suite %q is missing name", path)
	}
	return suite, nil
}
