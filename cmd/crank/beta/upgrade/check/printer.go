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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
)

// minWrapWidth is the floor for body wrapping. Below this we skip wrapping
// rather than emit tiny fragments.
const minWrapWidth = 40

// sidebarPrefix stamps every body line in a category section. The leading
// spaces align body content under the "[✗] " / "[!] " badge in the header.
const sidebarPrefix = "    │  "

const sidebarClose = "    └──"

// sidebarOverhead is the column width consumed by sidebarPrefix.
const sidebarOverhead = 7

// ANSI color codes. Emitted only when output is a terminal and NO_COLOR is
// unset (https://no-color.org).
const (
	ansiReset  = "\x1b[0m"
	ansiRed    = "\x1b[31m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
)

// colors decides whether ANSI codes are emitted. When disabled, wrap returns
// its input verbatim so call sites don't need to branch.
type colors struct {
	enabled bool
}

func detectColors(w io.Writer) colors {
	if os.Getenv("NO_COLOR") != "" {
		return colors{}
	}
	f, ok := w.(*os.File)
	if !ok {
		return colors{}
	}
	if _, _, err := term.GetSize(int(f.Fd())); err != nil {
		return colors{}
	}
	return colors{enabled: true}
}

func (c colors) wrap(code, s string) string {
	if !c.enabled {
		return s
	}
	return code + s + ansiReset
}

func (c colors) red(s string) string    { return c.wrap(ansiRed, s) }
func (c colors) yellow(s string) string { return c.wrap(ansiYellow, s) }
func (c colors) cyan(s string) string   { return c.wrap(ansiCyan, s) }

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

// TextPrinter renders a Report as a kubectl-style, human-readable summary.
type TextPrinter struct{}

// Print writes r to w.
func (p *TextPrinter) Print(w io.Writer, r Report) error {
	bodyWidth := detectBodyWidth(w)
	clr := detectColors(w)

	totalIssues := 0
	totalInfo := 0
	checksFailed := 0
	for _, c := range r.Categories {
		if c.Err != "" {
			checksFailed++
		}
		if c.Severity == SeverityInfo {
			totalInfo += len(c.Findings)
		} else {
			totalIssues += len(c.Findings)
		}
	}

	// Header: verdict badge plus a fixed
	// "N issues, M informational, K incomplete checks." breakdown.
	// Counts are always emitted and always colored.
	if len(r.Categories) > 0 {
		var badge string
		switch {
		case totalIssues > 0:
			badge = "[✗]"
		case checksFailed > 0:
			badge = "[!]"
		case totalInfo > 0:
			badge = "[i]"
		default:
			badge = "[✓]"
		}
		issuesFrag := clr.red(pluralize(totalIssues, "issue", "issues"))
		infoFrag := clr.cyan(pluralize(totalInfo, "informational", "informational"))
		failuresFrag := clr.yellow(pluralize(checksFailed, "incomplete check", "incomplete checks"))
		fmt.Fprintf(w, "%s %s, %s, %s.\n\n", badge, issuesFrag, infoFrag, failuresFrag)
	}

	for _, c := range r.Categories {
		// Healthy category: render as a single confirmation line. Description
		// / Fix / Docs are omitted intentionally - they describe what would
		// be wrong, and showing them next to a passing check reads as "you
		// should fix this."
		if len(c.Findings) == 0 && c.Err == "" {
			fmt.Fprintf(w, "[✓] %s\n", c.Title)
			continue
		}

		// A failed check beats findings in the badge: the findings list may
		// be partial when the check errored, so we surface the unknown rather
		// than imply exhaustiveness.
		switch {
		case c.Err != "":
			fmt.Fprintf(w, "[!] %s - check failed\n", c.Title)
		case c.Severity == SeverityInfo:
			fmt.Fprintf(w, "[i] %s - %s\n", c.Title, pluralize(len(c.Findings), "informational finding", "informational findings"))
		default:
			fmt.Fprintf(w, "[✗] %s - %s\n", c.Title, pluralize(len(c.Findings), "issue", "issues"))
		}

		pw := newLinePrefixWriter(w, sidebarPrefix)
		fmt.Fprintln(pw)
		if c.Description != "" {
			writeWrapped(pw, "", c.Description, bodyWidth)
		}
		if c.Err != "" {
			writeWrapped(pw, "Error: ", c.Err, bodyWidth)
		}
		if c.Remediation != "" {
			writeWrapped(pw, "Fix:   ", c.Remediation, bodyWidth)
		}
		if len(c.DocsURLs) > 0 {
			// One URL per line. The first carries the "Docs:" label; subsequent
			// lines indent under it. A single URL longer than the body width
			// still overflows the sidebar - wrapText won't break mid-word.
			docsIndent := strings.Repeat(" ", utf8.RuneCountInString("Docs:  "))
			for i, u := range c.DocsURLs {
				label := docsIndent
				if i == 0 {
					label = "Docs:  "
				}
				writeWrapped(pw, label, u, bodyWidth)
			}
		}

		if len(c.Findings) > 0 {
			fmt.Fprintln(pw)
			groups := groupByKind(c.Findings)
			for i, g := range groups {
				if i > 0 {
					fmt.Fprintln(pw)
				}
				printKindGroup(pw, g)
			}
		}

		fmt.Fprintln(w, sidebarClose)
	}

	return nil
}

// linePrefixWriter prepends a fixed prefix to each line written through it,
// so category sections render with a consistent left-edge sidebar.
type linePrefixWriter struct {
	w       io.Writer
	prefix  string
	pending bool
}

func newLinePrefixWriter(w io.Writer, prefix string) *linePrefixWriter {
	return &linePrefixWriter{w: w, prefix: prefix, pending: true}
}

func (lw *linePrefixWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		if lw.pending {
			if _, err := io.WriteString(lw.w, lw.prefix); err != nil {
				return written, err
			}
			lw.pending = false
		}
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			n, err := lw.w.Write(p)
			return written + n, err
		}
		n, err := lw.w.Write(p[:i+1])
		written += n
		if err != nil {
			return written, err
		}
		p = p[i+1:]
		lw.pending = true
	}
	return written, nil
}

// kindGroup is a slice of findings sharing the same Kind+APIVersion, used to
// render per-type sub-tables.
type kindGroup struct {
	kind       string
	apiVersion string
	findings   []Finding
}

func groupByKind(fs []Finding) []kindGroup {
	seen := map[string]int{}
	var out []kindGroup
	for _, f := range fs {
		key := f.Resource.APIVersion + "|" + f.Resource.Kind
		if i, ok := seen[key]; ok {
			out[i].findings = append(out[i].findings, f)
			continue
		}
		seen[key] = len(out)
		out = append(out, kindGroup{
			kind:       f.Resource.Kind,
			apiVersion: f.Resource.APIVersion,
			findings:   []Finding{f},
		})
	}
	return out
}

func printKindGroup(w io.Writer, g kindGroup) {
	namespaced := false
	for _, f := range g.findings {
		if f.Resource.Namespace != "" {
			namespaced = true
			break
		}
	}

	tw := tabwriter.NewWriter(w, 0, 4, 3, ' ', 0)
	if namespaced {
		fmt.Fprintln(tw, "  NAMESPACE\tNAME\tFIELD")
	} else {
		fmt.Fprintln(tw, "  NAME\tFIELD")
	}
	for _, f := range g.findings {
		// Sanitize cluster-supplied strings: a resource named "foo[2J" or
		// containing tabs would corrupt the terminal output.
		name := resourceTypePrefix(f.Resource.APIVersion, f.Resource.Kind) + "/" + sanitize(f.Resource.Name)
		field := f.FieldPath
		if field == "" {
			field = "-"
		}
		if namespaced {
			ns := sanitize(f.Resource.Namespace)
			if ns == "" {
				ns = "-"
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", ns, name, field)
		} else {
			fmt.Fprintf(tw, "  %s\t%s\n", name, field)
		}
	}
	tw.Flush()
}

// sanitize replaces any non-printable rune in s with U+FFFD, keeping terminal
// escapes and tabs from cluster-sourced strings out of the rendered output.
func sanitize(s string) string {
	if s == "" {
		return s
	}
	hasBad := false
	for _, r := range s {
		if !unicode.IsPrint(r) {
			hasBad = true
			break
		}
	}
	if !hasBad {
		return s
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if unicode.IsPrint(r) {
			out = append(out, r)
			continue
		}
		out = append(out, '�')
	}
	return string(out)
}

// resourceTypePrefix returns the kubectl-style "kind.group" prefix (e.g.
// "composition.apiextensions.crossplane.io"). Core types without a group
// render as just the lowercase kind.
func resourceTypePrefix(apiVersion, kind string) string {
	parts := strings.SplitN(apiVersion, "/", 2)
	group := ""
	if len(parts) == 2 {
		group = parts[0]
	}
	name := strings.ToLower(kind)
	if group == "" {
		return name
	}
	return name + "." + group
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// detectBodyWidth returns the column width available for wrapped body lines
// (terminal width minus sidebar prefix). Returns 0 when the output isn't a
// terminal, so long lines stay intact for grep and other downstream tools.
func detectBodyWidth(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok {
		return 0
	}
	cols, _, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return 0
	}
	cols -= sidebarOverhead
	if cols < minWrapWidth {
		return 0
	}
	return cols
}

// writeWrapped writes label followed by body, word-wrapping body to maxWidth
// columns and indenting continuation lines under the label. maxWidth <= 0
// disables wrapping.
func writeWrapped(w io.Writer, label, body string, maxWidth int) {
	if maxWidth <= 0 {
		fmt.Fprintf(w, "%s%s\n", label, body)
		return
	}
	labelLen := utf8.RuneCountInString(label)
	contentWidth := maxWidth - labelLen
	if contentWidth < minWrapWidth {
		fmt.Fprintf(w, "%s%s\n", label, body)
		return
	}
	lines := wrapText(body, contentWidth)
	if len(lines) == 0 {
		fmt.Fprintln(w, label)
		return
	}
	indent := strings.Repeat(" ", labelLen)
	fmt.Fprintln(w, label+lines[0])
	for _, l := range lines[1:] {
		fmt.Fprintln(w, indent+l)
	}
}

// wrapText word-wraps s into lines no wider than maxWidth runes. Words
// longer than maxWidth (typically URLs) go on their own line and overflow
// rather than break - a broken URL is worse than one that wraps.
func wrapText(s string, maxWidth int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	var cur strings.Builder
	curLen := 0
	for _, word := range words {
		wlen := utf8.RuneCountInString(word)
		switch {
		case curLen == 0:
			cur.WriteString(word)
			curLen = wlen
		case curLen+1+wlen > maxWidth:
			lines = append(lines, cur.String())
			cur.Reset()
			cur.WriteString(word)
			curLen = wlen
		default:
			cur.WriteByte(' ')
			cur.WriteString(word)
			curLen += 1 + wlen
		}
	}
	if curLen > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

// JSONPrinter emits the report as pretty-printed JSON.
type JSONPrinter struct{}

// Print writes r to w.
func (p *JSONPrinter) Print(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
