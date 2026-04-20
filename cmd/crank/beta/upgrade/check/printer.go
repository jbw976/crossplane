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

// minWrapWidth is the floor for wrapping. Below this, wrapping makes the
// output worse (tiny line fragments), so we fall back to "no wrap" — the
// sidebar may still break visually on very narrow terminals, but that's
// strictly better than mangling the prose.
const minWrapWidth = 40

// sidebarPrefix is stamped on every body line of a category section. The four
// leading spaces align it under the "[✗] " / "[!] " badge in the header, so
// the body visually belongs to the badge above it.
const sidebarPrefix = "    │  "

// sidebarClose closes a category section, aligned with sidebarPrefix.
const sidebarClose = "    └──"

// sidebarOverhead is the column width consumed by sidebarPrefix (used to
// compute remaining body width for word wrapping).
const sidebarOverhead = 7

// ANSI color codes used by the text printer. Emitted only when output is a
// terminal and the NO_COLOR env var is unset (see https://no-color.org).
const (
	ansiReset  = "\x1b[0m"
	ansiRed    = "\x1b[31m"
	ansiYellow = "\x1b[33m"
)

// colors decides whether ANSI codes are emitted. Disabled means every wrap
// call returns the input string verbatim, so call sites don't need to branch.
type colors struct {
	enabled bool
}

// detectColors enables color when w is a TTY and NO_COLOR is unset. Anything
// else (piped to file, redirected, or NO_COLOR set) gets plain text.
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

// TextPrinter renders a Report as a human-readable, kubectl-style report. Each
// category becomes a section: a title with issue count, the category-level
// description, a one-line remediation, an optional docs URL, and a tabular
// list of offending resources.
type TextPrinter struct{}

// Print renders the report.
func (p *TextPrinter) Print(w io.Writer, r Report) error {
	bodyWidth := detectBodyWidth(w)
	clr := detectColors(w)

	totalIssues := 0
	checksFailed := 0
	for _, c := range r.Categories {
		totalIssues += len(c.Findings)
		if c.Err != "" {
			checksFailed++
		}
	}

	// Header summary in vale-style: a verdict badge followed by a fixed
	// "N issues, K incomplete checks." breakdown. Both counts always appear
	// so the format is consistent across runs; non-zero counts are colored to
	// draw the eye, zeros render plain.
	if len(r.Categories) > 0 {
		var badge string
		switch {
		case totalIssues > 0:
			badge = "[✗]"
		case checksFailed > 0:
			badge = "[!]"
		default:
			badge = "[✓]"
		}
		issuesFrag := clr.red(pluralize(totalIssues, "issue", "issues"))
		failuresFrag := clr.yellow(pluralize(checksFailed, "incomplete check", "incomplete checks"))
		fmt.Fprintf(w, "%s %s, %s.\n\n", badge, issuesFrag, failuresFrag)
	}

	for _, c := range r.Categories {
		// Healthy category: ran cleanly, nothing to report. Render as a single
		// confirmation line so the report is a complete checklist of every
		// category that ran. We deliberately omit Description / Fix / Docs
		// here — those describe what *would* be wrong, and showing them next
		// to a passing check reads as "you should fix this," which is wrong.
		if len(c.Findings) == 0 && c.Err == "" {
			fmt.Fprintf(w, "[✓] %s\n", c.Title)
			continue
		}

		// A failed check takes precedence over findings in the badge: the
		// findings list may be partial (or empty) when the check errored, so
		// the user can't trust it as exhaustive. Surface the unknown state
		// rather than implying "we found these N things and that's all."
		switch {
		case c.Err != "":
			fmt.Fprintf(w, "[!] %s — check failed\n", c.Title)
		default:
			fmt.Fprintf(w, "[✗] %s — %s\n", c.Title, pluralize(len(c.Findings), "issue", "issues"))
		}

		// Body lines are written through a sidebar-prefixing writer so each
		// line in the section visibly belongs to it. The prefix is constant
		// width, so tabwriter alignment within tables is preserved.
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
		if c.DocsURL != "" {
			// URLs aren't safely word-wrappable; emit as-is and accept that
			// a very long URL on a narrow terminal may break the sidebar.
			fmt.Fprintf(pw, "Docs:  %s\n", c.DocsURL)
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

// linePrefixWriter wraps an io.Writer and prepends a fixed prefix to each line
// it writes through. We use it so a category section can render with a
// consistent left-edge sidebar without every call site having to know about
// the prefix.
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

// kindGroup is a slice of findings sharing the same Kind+APIVersion. Used to
// render kubectl-style per-type sub-tables within a category section.
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
		// Sanitize cluster-supplied strings before writing them into a
		// human-readable terminal stream. A resource named "foo[2J" or
		// containing tabs would otherwise corrupt the output.
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

// sanitize replaces any non-printable rune in s with U+FFFD. We use this on
// strings sourced from the cluster (resource names, namespaces) before
// rendering them in the text printer to keep terminal escape sequences and
// tabs out of the output.
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

// resourceTypePrefix returns the kubectl-style "kind.group" prefix for a
// resource, mirroring how kubectl renders names when querying multiple types
// (e.g. "composition.apiextensions.crossplane.io"). Core API types (apiVersion
// without a group, like "v1") render as just the lowercase kind.
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
// (i.e. terminal width minus the sidebar prefix). Returns 0 when wrapping
// should be skipped — typically because output isn't a terminal (piped to a
// file or another process), in which case we want to preserve the original
// long lines for downstream consumers like grep.
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

// writeWrapped writes label followed by body, word-wrapped so each emitted
// line fits within maxWidth columns. Continuation lines are indented to
// align past the label so the wrapped prose reads as a block. When
// maxWidth <= 0 we don't wrap at all and emit the line as-is.
func writeWrapped(w io.Writer, label, body string, maxWidth int) {
	if maxWidth <= 0 {
		fmt.Fprintf(w, "%s%s\n", label, body)
		return
	}
	labelLen := utf8.RuneCountInString(label)
	contentWidth := maxWidth - labelLen
	if contentWidth < minWrapWidth {
		// Label leaves no room for meaningful wrapping; emit unwrapped.
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

// wrapText word-wraps s into lines no wider than maxWidth runes. Words longer
// than maxWidth (typically URLs) are placed on their own line and allowed to
// overflow rather than mid-word-broken; that's a deliberate trade-off — a
// broken URL is less useful than one that wraps in the terminal.
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

// JSONPrinter emits the full report as JSON.
type JSONPrinter struct{}

// Print writes the report as pretty-printed JSON.
func (p *JSONPrinter) Print(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
