/*
Copyright 2025 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package check implements the `crossplane beta upgrade check` command.
package check

import (
	"context"
	"sort"
	"sync"

	"github.com/crossplane/crossplane-runtime/pkg/logging"
)

// Severity indicates how significant a finding is.
type Severity string

// Known severity levels. Error findings cause non-zero exit.
const (
	SeverityInfo    Severity = "Info"
	SeverityWarning Severity = "Warning"
	SeverityError   Severity = "Error"
)

// ResourceRef identifies the offending resource.
type ResourceRef struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name,omitempty"`
}

// Finding describes one thing the user should address before upgrading.
type Finding struct {
	Category    string      `json:"category"`
	Severity    Severity    `json:"severity"`
	Resource    ResourceRef `json:"resource,omitempty"`
	FieldPath   string      `json:"fieldPath,omitempty"`
	Message     string      `json:"message"`
	Remediation string      `json:"remediation,omitempty"`
}

// CategoryResult groups findings by the check that produced them.
type CategoryResult struct {
	Category    string    `json:"category"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Findings    []Finding `json:"findings,omitempty"`
	Err         string    `json:"error,omitempty"`
}

// Report is the aggregate output of a run.
type Report struct {
	Categories []CategoryResult `json:"categories"`
}

// HasErrors reports whether any Error-severity findings were produced. Used to
// drive the process exit code.
func (r Report) HasErrors() bool {
	for _, c := range r.Categories {
		for _, f := range c.Findings {
			if f.Severity == SeverityError {
				return true
			}
		}
	}
	return false
}

// Check is a single upgrade compatibility check.
type Check interface {
	// Category is a stable, machine-friendly identifier for the check.
	Category() string
	// Title is a short human-readable title shown in output.
	Title() string
	// Description explains what the check looks for.
	Description() string
	// Run executes the check and returns any findings.
	Run(ctx context.Context) ([]Finding, error)
}

// Runner executes checks and aggregates results.
type Runner struct {
	Checks []Check
	Logger logging.Logger
}

// Run executes all checks concurrently.
func (r *Runner) Run(ctx context.Context) (Report, error) {
	results := make([]CategoryResult, len(r.Checks))
	var wg sync.WaitGroup
	for i, c := range r.Checks {
		wg.Add(1)
		go func(i int, c Check) {
			defer wg.Done()
			r.Logger.Debug("running check", "category", c.Category())
			res := CategoryResult{
				Category:    c.Category(),
				Title:       c.Title(),
				Description: c.Description(),
			}
			findings, err := c.Run(ctx)
			if err != nil {
				res.Err = err.Error()
				r.Logger.Debug("check failed", "category", c.Category(), "error", err)
			}
			sort.SliceStable(findings, func(a, b int) bool {
				if findings[a].Resource.Kind != findings[b].Resource.Kind {
					return findings[a].Resource.Kind < findings[b].Resource.Kind
				}
				if findings[a].Resource.Namespace != findings[b].Resource.Namespace {
					return findings[a].Resource.Namespace < findings[b].Resource.Namespace
				}
				return findings[a].Resource.Name < findings[b].Resource.Name
			})
			res.Findings = findings
			results[i] = res
		}(i, c)
	}
	wg.Wait()
	return Report{Categories: results}, nil
}
