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

// Severity classifies findings by whether they block an upgrade.
type Severity string

const (
	// SeverityError marks findings that must be addressed before upgrading.
	// Error-severity findings cause the command to exit non-zero.
	SeverityError Severity = "error"
	// SeverityInfo marks findings that are informational only. The underlying
	// behavior keeps working after the upgrade; the finding surfaces work the
	// user may want to do for forward-looking migration. Info findings do not
	// cause a non-zero exit.
	SeverityInfo Severity = "info"
)

// ResourceRef identifies the offending resource.
type ResourceRef struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name,omitempty"`
}

// Finding describes one thing the user must address before upgrading.
// Description and Remediation live on the category, not the finding.
type Finding struct {
	Category  string      `json:"category"`
	Resource  ResourceRef `json:"resource,omitempty"`
	FieldPath string      `json:"fieldPath,omitempty"`
}

// CategoryResult groups findings produced by a single Check.
type CategoryResult struct {
	Category    string    `json:"category"`
	Title       string    `json:"title"`
	Severity    Severity  `json:"severity,omitempty"`
	Description string    `json:"description"`
	Remediation string    `json:"remediation,omitempty"`
	DocsURLs    []string  `json:"docsURLs,omitempty"`
	Findings    []Finding `json:"findings,omitempty"`
	Err         string    `json:"error,omitempty"`
}

// Report is the aggregate output of a run.
type Report struct {
	Categories []CategoryResult `json:"categories"`
}

// HasErrors reports whether the run produced anything that should trigger a
// non-zero exit: any error-severity finding, or a check that failed to
// execute. Info-severity findings do not count. A failed check counts because
// the user is left with incomplete information.
func (r Report) HasErrors() bool {
	for _, c := range r.Categories {
		if c.Err != "" {
			return true
		}
		if c.Severity != SeverityInfo && len(c.Findings) > 0 {
			return true
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
	// Severity is the severity of findings produced by this check.
	Severity() Severity
	// Description explains what the check looks for.
	Description() string
	// Remediation is one-line, action-oriented advice for the whole category.
	// Must not contain URLs - see DocsURLs.
	Remediation() string
	// DocsURLs returns documentation links for the category. Empty when none apply.
	DocsURLs() []string
	// Run executes the check and returns any findings.
	Run(ctx context.Context) ([]Finding, error)
}

// Runner executes checks and aggregates results.
type Runner struct {
	Checks []Check
	Logger logging.Logger
}

// Run executes all checks concurrently. Errors from individual checks are
// captured on each CategoryResult.Err; the Runner itself does not fail.
func (r *Runner) Run(ctx context.Context) Report {
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
				Severity:    c.Severity(),
				Description: c.Description(),
				Remediation: c.Remediation(),
				DocsURLs:    c.DocsURLs(),
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
				if findings[a].Resource.Name != findings[b].Resource.Name {
					return findings[a].Resource.Name < findings[b].Resource.Name
				}
				return findings[a].FieldPath < findings[b].FieldPath
			})
			res.Findings = findings
			results[i] = res
		}(i, c)
	}
	wg.Wait()
	return Report{Categories: results}
}
