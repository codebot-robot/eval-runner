// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ui

import (
	"reflect"
	"strings"
	"testing"
)

// structuredReasonMessage follows the sectioned message format: a headline, blank-line
// separated blocks, "Heading:" lines above "- " bullets, and a closing link.
const structuredReasonMessage = `Pass rate fell on 2 of 4 tasks and rose on 1; the largest drop is docs/readme_comprehension, 80% -> 40%.

Every task has 5 paired samples, below the 8 pairs needed to label a task, so all tasks are inconclusive.
The headline P(improvement) is the lowest per-task value.

Largest drops:
- docs/readme_comprehension 80% -> 40% (3 timeouts)
- triage/runbook_lookup 70% -> 54% (1 incorrect)

Candidate failures:
- 4 timeouts at 120 s
- 1 incorrect <b>answer</b> & "quoted" text

Code changes: https://github.com/example/repo/compare/a81d0c4...05ce4d8`

// segmentsText flattens segments for compact assertions; links render as [text](url).
func segmentsText(segments []ReasonSegment) string {
	var b strings.Builder
	for _, s := range segments {
		if s.URL != "" {
			b.WriteString("[" + s.Text + "](" + s.URL + ")")
		} else {
			b.WriteString(s.Text)
		}
	}
	return b.String()
}

func TestParseReason_Structured(t *testing.T) {
	view := ParseReason(structuredReasonMessage)

	if view.Plain != nil {
		t.Fatalf("expected a structured view, got Plain=%v", view.Plain)
	}
	if got, want := segmentsText(view.Headline), "Pass rate fell on 2 of 4 tasks and rose on 1; the largest drop is docs/readme_comprehension, 80% -> 40%."; got != want {
		t.Errorf("headline = %q, want %q", got, want)
	}
	if len(view.Blocks) != 4 {
		t.Fatalf("expected 4 blocks, got %d: %+v", len(view.Blocks), view.Blocks)
	}

	// Block 1: one paragraph that keeps its two lines.
	if parts := view.Blocks[0].Parts; len(parts) != 1 || len(parts[0].Lines) != 2 {
		t.Fatalf("expected one two-line paragraph, got %+v", parts)
	}
	if got := segmentsText(view.Blocks[0].Parts[0].Lines[1]); got != "The headline P(improvement) is the lowest per-task value." {
		t.Errorf("second paragraph line = %q", got)
	}

	// Blocks 2 and 3: a heading followed by a bullet list.
	for i, wantHeading := range []string{"Largest drops:", "Candidate failures:"} {
		parts := view.Blocks[i+1].Parts
		if len(parts) != 2 || parts[0].Heading == nil || len(parts[1].Items) != 2 {
			t.Fatalf("block %d: expected heading and two bullets, got %+v", i+1, parts)
		}
		if got := segmentsText(parts[0].Heading); got != wantHeading {
			t.Errorf("block %d heading = %q, want %q", i+1, got, wantHeading)
		}
	}
	if got := segmentsText(view.Blocks[2].Parts[1].Items[1]); got != `1 incorrect <b>answer</b> & "quoted" text` {
		t.Errorf("bullet text = %q", got)
	}

	// Block 4: the closing line with a link.
	last := view.Blocks[3].Parts
	if len(last) != 1 || len(last[0].Lines) != 1 {
		t.Fatalf("expected a one-line closing paragraph, got %+v", last)
	}
	if got, want := segmentsText(last[0].Lines[0]), "Code changes: [https://github.com/example/repo/compare/a81d0c4...05ce4d8](https://github.com/example/repo/compare/a81d0c4...05ce4d8)"; got != want {
		t.Errorf("closing line = %q, want %q", got, want)
	}
}

func TestParseReason_SingleParagraph(t *testing.T) {
	view := ParseReason("  Running batch 2 (1 tasks continuing)  ")
	if view.Headline != nil || view.Blocks != nil {
		t.Fatalf("expected a plain view, got %+v", view)
	}
	if !reflect.DeepEqual(view.Plain, []ReasonSegment{{Text: "Running batch 2 (1 tasks continuing)"}}) {
		t.Errorf("Plain = %+v", view.Plain)
	}
	if got := ParseReason(""); !reflect.DeepEqual(got, ReasonView{}) {
		t.Errorf("expected an empty view for an empty message, got %+v", got)
	}
}

func TestParseReason_Edges(t *testing.T) {
	t.Run("heading without bullets stays a paragraph line", func(t *testing.T) {
		view := ParseReason("Headline\n\nNotes:\nnot a bullet")
		parts := view.Blocks[0].Parts
		if len(parts) != 1 || len(parts[0].Lines) != 2 || parts[0].Heading != nil {
			t.Fatalf("expected one two-line paragraph, got %+v", parts)
		}
	})

	t.Run("paragraph, list, paragraph in one block", func(t *testing.T) {
		view := ParseReason("Headline\nintro line\n- a\n- b\noutro line")
		parts := view.Blocks[0].Parts
		if len(parts) != 3 || len(parts[0].Lines) != 1 || len(parts[1].Items) != 2 || len(parts[2].Lines) != 1 {
			t.Fatalf("expected paragraph, list, paragraph, got %+v", parts)
		}
	})

	t.Run("CRLF and repeated blank lines", func(t *testing.T) {
		view := ParseReason("Headline\r\n\r\n\r\nBlock one\r\n  \r\nBlock two\r\n")
		if got := segmentsText(view.Headline); got != "Headline" {
			t.Errorf("headline = %q", got)
		}
		if len(view.Blocks) != 2 {
			t.Fatalf("expected 2 blocks, got %d", len(view.Blocks))
		}
	})

	t.Run("indented bullets", func(t *testing.T) {
		view := ParseReason("Headline\nGroup:\n  - one\n  - two")
		parts := view.Blocks[0].Parts
		if len(parts) != 2 || parts[0].Heading == nil || len(parts[1].Items) != 2 || segmentsText(parts[1].Items[0]) != "one" {
			t.Fatalf("expected heading and two bullets, got %+v", parts)
		}
	})
}

func TestReasonHeadline(t *testing.T) {
	if got := ReasonHeadline("\n\n  First line  \nsecond"); got != "First line" {
		t.Errorf("ReasonHeadline() = %q", got)
	}
	if got := ReasonHeadline("only line"); got != "only line" {
		t.Errorf("ReasonHeadline() = %q", got)
	}
}

func TestLinkSegments(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "no URL", in: "plain text", want: "plain text"},
		{name: "empty", in: "", want: ""},
		{name: "trailing period stays outside", in: "See https://example.com/a/b.", want: "See [https://example.com/a/b](https://example.com/a/b)."},
		{name: "dots inside are kept", in: "https://github.com/o/r/compare/a...b", want: "[https://github.com/o/r/compare/a...b](https://github.com/o/r/compare/a...b)"},
		{name: "unbalanced closing parenthesis", in: "(https://example.com/x)", want: "([https://example.com/x](https://example.com/x))"},
		{name: "balanced parentheses", in: "https://en.example.org/wiki/Go_(language)", want: "[https://en.example.org/wiki/Go_(language)](https://en.example.org/wiki/Go_(language))"},
		{name: "two URLs", in: "a http://x.example b https://y.example/z, c", want: "a [http://x.example](http://x.example) b [https://y.example/z](https://y.example/z), c"},
		{name: "quote ends the URL", in: `https://x.example/a"><script>alert(1)</script>`, want: `[https://x.example/a](https://x.example/a)"><script>alert(1)</script>`},
		{name: "scheme without host", in: "see https:// later", want: "see https:// later"},
		{name: "non-http scheme", in: "javascript:alert(1) and ftp://example.com", want: "javascript:alert(1) and ftp://example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := segmentsText(LinkSegments(tt.in)); got != tt.want {
				t.Errorf("LinkSegments(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
