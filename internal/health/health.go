// Package health reports Ken's readiness as a small set of components (the data
// dir is writable, the database answers a ping), in the Actuator-style JSON shape
// {"status":"UP","components":{…}}. It is deliberately tiny and dependency-free.
package health

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"time"
)

// PingFunc probes a dependency (e.g. the database) within the given context.
type PingFunc func(context.Context) error

// Checker aggregates component checks into one readiness report.
type Checker struct {
	dataDir string
	pings   map[string]PingFunc
}

// New builds a Checker whose storage component verifies dataDir is writable.
func New(dataDir string) *Checker {
	return &Checker{dataDir: dataDir, pings: map[string]PingFunc{}}
}

// AddPing registers a named dependency probe (e.g. "db").
func (c *Checker) AddPing(name string, f PingFunc) { c.pings[name] = f }

// Component is one dependency's status plus optional operator-only details.
type Component struct {
	Status  string         `json:"status"`
	Details map[string]any `json:"details,omitempty"`
}

// Report is the overall readiness result. Status is "UP" only when every
// component is UP; a single DOWN flips the whole report to "DOWN".
type Report struct {
	Status     string               `json:"status"`
	Components map[string]Component `json:"components"`
}

// Check evaluates every component (each probe bounded to 2s) and composes the
// overall status.
func (c *Checker) Check(ctx context.Context) Report {
	comps := make(map[string]Component, len(c.pings)+1)
	overall := "UP"

	for name, f := range c.pings {
		comp := Component{Status: "UP"}
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := f(cctx)
		cancel()
		if err != nil {
			comp.Status = "DOWN"
			comp.Details = map[string]any{"error": err.Error()}
			overall = "DOWN"
		}
		comps[name] = comp
	}

	storage := Component{Status: "UP", Details: map[string]any{"path": c.dataDir, "writable": true}}
	if err := checkWritable(c.dataDir); err != nil {
		storage.Status = "DOWN"
		storage.Details["writable"] = false
		storage.Details["error"] = err.Error()
		overall = "DOWN"
	}
	comps["storage"] = storage

	return Report{Status: overall, Components: comps}
}

// checkWritable proves the directory accepts writes by creating and removing a
// temp file — turning the probe from liveness into a real readiness signal.
func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".ken-health-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// WriteJSON encodes the report. When showDetails is false (an anonymous caller)
// only per-component status is emitted, so paths/errors never leak publicly —
// the "show-details: when-authorized" posture.
func (r Report) WriteJSON(w io.Writer, showDetails bool) {
	if !showDetails {
		stripped := Report{Status: r.Status, Components: make(map[string]Component, len(r.Components))}
		for k, c := range r.Components {
			stripped.Components[k] = Component{Status: c.Status}
		}
		r = stripped
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(r)
}
