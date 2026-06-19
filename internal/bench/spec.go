package bench

import (
	"encoding/json"
	"fmt"
	"os"
)

type Suite struct {
	Name            string         `json:"name"`
	Description     string         `json:"description"`
	DurationMinutes int            `json:"duration_minutes"`
	Harness         Harness        `json:"harness"`
	Workloads       []Workload     `json:"workloads"`
	Sweeps          []Sweep        `json:"sweeps"`
	OverallBurst    *BurstEnvelope `json:"overall_burst,omitempty"`
}

// BurstEnvelope shapes the macro-level arrival rate of the mixed "overall"
// replay so that every workload surges and quiets together. It only affects
// the merged overall trace; per-workload traces are left at their native
// arrival pattern. When a suite leaves this nil, a default envelope is used.
type BurstEnvelope struct {
	Baseline   float64         `json:"baseline,omitempty"`    // trough-level rate multiplier
	DiurnalAmp float64         `json:"diurnal_amp,omitempty"` // amplitude of the slow cosine ramp
	Spikes     []EnvelopeSpike `json:"spikes,omitempty"`      // coordinated surges
}

// EnvelopeSpike is a Gaussian surge added to the envelope. At and Width are
// fractions of the run [0,1]; Amp is the added peak height.
type EnvelopeSpike struct {
	At    float64 `json:"at"`
	Width float64 `json:"width"`
	Amp   float64 `json:"amp"`
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
	TraceMode       string `json:"trace_mode,omitempty"`
	WindowSeconds   int    `json:"window_seconds,omitempty"`
	Selection       string `json:"selection,omitempty"`
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
