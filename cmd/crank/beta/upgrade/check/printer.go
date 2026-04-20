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

package check

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
)

// Printer renders a Report.
type Printer interface {
	Print(w io.Writer, r Report) error
}

// NewPrinter returns a Printer for the named output format.
func NewPrinter(format string) (Printer, error) {
	switch format {
	case "text", "":
		return &TextPrinter{}, nil
	case "json":
		return &JSONPrinter{}, nil
	default:
		return nil, errors.Errorf("unknown output format %q", format)
	}
}

// TextPrinter prints a human-readable report.
type TextPrinter struct{}

// Print renders the report as grouped text.
func (p *TextPrinter) Print(w io.Writer, r Report) error {
	total := 0
	for _, c := range r.Categories {
		total += len(c.Findings)
	}

	if total == 0 {
		for _, c := range r.Categories {
			if c.Err != "" {
				fmt.Fprintf(w, "[!] %s: check failed: %s\n", c.Title, c.Err)
			}
		}
		fmt.Fprintln(w, "No upgrade-blocking issues found.")
		return nil
	}

	fmt.Fprintf(w, "Found %d issue(s) across %d categories:\n\n", total, countCategoriesWithFindings(r))
	for _, c := range r.Categories {
		if len(c.Findings) == 0 && c.Err == "" {
			continue
		}
		fmt.Fprintf(w, "== %s (%d) ==\n", c.Title, len(c.Findings))
		if c.Description != "" {
			fmt.Fprintf(w, "%s\n", c.Description)
		}
		if c.Err != "" {
			fmt.Fprintf(w, "  [!] check failed: %s\n", c.Err)
		}
		for _, f := range c.Findings {
			ref := f.Resource.Kind
			if f.Resource.APIVersion != "" {
				ref = fmt.Sprintf("%s.%s", f.Resource.Kind, f.Resource.APIVersion)
			}
			name := f.Resource.Name
			if f.Resource.Namespace != "" {
				name = f.Resource.Namespace + "/" + name
			}
			fmt.Fprintf(w, "  [%s] %s %q", f.Severity, ref, name)
			if f.FieldPath != "" {
				fmt.Fprintf(w, " at %s", f.FieldPath)
			}
			fmt.Fprintln(w)
			if f.Message != "" {
				fmt.Fprintf(w, "      %s\n", f.Message)
			}
			if f.Remediation != "" {
				fmt.Fprintf(w, "      -> %s\n", f.Remediation)
			}
		}
		fmt.Fprintln(w)
	}
	return nil
}

func countCategoriesWithFindings(r Report) int {
	n := 0
	for _, c := range r.Categories {
		if len(c.Findings) > 0 {
			n++
		}
	}
	return n
}

// JSONPrinter emits the full report as JSON.
type JSONPrinter struct{}

// Print writes the report as pretty-printed JSON.
func (p *JSONPrinter) Print(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
