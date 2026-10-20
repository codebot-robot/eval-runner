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
	"net/url"
	"regexp"
	"strings"
)

// ReasonSegment is a run of text inside one line of a Comparison message. When URL is set,
// the segment renders as a link whose visible text is Text. Templates render both fields
// through html/template, so message text is always escaped.
type ReasonSegment struct {
	Text string
	URL  string
}

// ReasonPart is one element of a ReasonBlock. Exactly one of Heading, Items or Lines is set:
// a block heading, a bullet list, or paragraph lines that keep their line breaks.
type ReasonPart struct {
	Heading []ReasonSegment
	Items   [][]ReasonSegment
	Lines   [][]ReasonSegment
}

// ReasonBlock is one blank-line-separated block of a multi-line Comparison message.
type ReasonBlock struct {
	Parts []ReasonPart
}

// ReasonView is a Comparison's status.message prepared for the Reason panel.
//
// A message without line breaks fills Plain and renders as a single text block. A message
// with line breaks fills Headline with its first line and Blocks with the rest.
type ReasonView struct {
	Plain    []ReasonSegment
	Headline []ReasonSegment
	Blocks   []ReasonBlock
}

// ReasonBody parses the view's Message for the Reason panel on the Comparison detail page.
func (v ComparisonDetailView) ReasonBody() ReasonView {
	return ParseReason(v.Message)
}

// reasonURLPattern matches http and https URLs up to whitespace, quotes or angle brackets.
var reasonURLPattern = regexp.MustCompile("https?://[^\\s<>\"'`]+")

// normalizeNewlines converts CRLF and lone CR line endings to LF.
func normalizeNewlines(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// ReasonHeadline returns the first non-empty line of msg with surrounding whitespace removed.
func ReasonHeadline(msg string) string {
	for _, line := range strings.Split(normalizeNewlines(msg), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// ParseReason splits a Comparison message into the structure the Reason panel renders:
//
//   - The first line is the headline.
//   - Blank lines separate blocks.
//   - Consecutive lines that start with "- " form a bullet list.
//   - A non-bullet line that ends in ":" and comes directly before a bullet is a block heading.
//   - Other lines are paragraph lines that keep their line breaks.
//
// Text that looks like an http or https URL becomes a link segment. A message without line
// breaks is returned as Plain so that it renders the same way as an unstructured message.
func ParseReason(msg string) ReasonView {
	msg = strings.TrimSpace(normalizeNewlines(msg))
	if msg == "" {
		return ReasonView{}
	}
	if !strings.Contains(msg, "\n") {
		return ReasonView{Plain: LinkSegments(msg)}
	}

	lines := strings.Split(msg, "\n")
	view := ReasonView{Headline: LinkSegments(strings.TrimSpace(lines[0]))}

	var block []string
	flush := func() {
		if len(block) > 0 {
			view.Blocks = append(view.Blocks, parseReasonBlock(block))
			block = nil
		}
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		block = append(block, strings.TrimRight(line, " \t"))
	}
	flush()
	return view
}

// reasonBullet returns the text of a bullet line without its "- " marker.
func reasonBullet(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if strings.HasPrefix(trimmed, "- ") {
		return strings.TrimSpace(trimmed[2:]), true
	}
	return "", false
}

// parseReasonBlock groups the lines of one block into headings, bullet lists and paragraphs.
func parseReasonBlock(lines []string) ReasonBlock {
	var block ReasonBlock
	for i := 0; i < len(lines); {
		if _, ok := reasonBullet(lines[i]); ok {
			var items [][]ReasonSegment
			for ; i < len(lines); i++ {
				text, ok := reasonBullet(lines[i])
				if !ok {
					break
				}
				items = append(items, LinkSegments(text))
			}
			block.Parts = append(block.Parts, ReasonPart{Items: items})
			continue
		}

		line := strings.TrimSpace(lines[i])
		if strings.HasSuffix(line, ":") && i+1 < len(lines) {
			if _, nextIsBullet := reasonBullet(lines[i+1]); nextIsBullet {
				block.Parts = append(block.Parts, ReasonPart{Heading: LinkSegments(line)})
				i++
				continue
			}
		}

		// Paragraph lines are appended to the preceding paragraph so line breaks are kept.
		if n := len(block.Parts); n > 0 && block.Parts[n-1].Lines != nil {
			block.Parts[n-1].Lines = append(block.Parts[n-1].Lines, LinkSegments(line))
		} else {
			block.Parts = append(block.Parts, ReasonPart{Lines: [][]ReasonSegment{LinkSegments(line)}})
		}
		i++
	}
	return block
}

// LinkSegments splits one line into text and link segments. Only absolute http and https URLs
// with a host become links. Trailing sentence punctuation and unbalanced closing brackets stay
// in the following text segment.
func LinkSegments(line string) []ReasonSegment {
	var segments []ReasonSegment
	pos := 0
	for _, match := range reasonURLPattern.FindAllStringIndex(line, -1) {
		start := match[0]
		if start < pos {
			continue
		}
		link := trimURLPunctuation(line[start:match[1]])
		if u, err := url.Parse(link); err != nil || u.Host == "" {
			continue
		}
		if start > pos {
			segments = append(segments, ReasonSegment{Text: line[pos:start]})
		}
		segments = append(segments, ReasonSegment{Text: link, URL: link})
		pos = start + len(link)
	}
	if pos < len(line) || len(segments) == 0 {
		segments = append(segments, ReasonSegment{Text: line[pos:]})
	}
	return segments
}

// trimURLPunctuation drops trailing characters that usually end the surrounding sentence
// rather than the URL: sentence punctuation, and closing brackets without a matching opener.
func trimURLPunctuation(s string) string {
	for len(s) > 0 {
		switch last := s[len(s)-1]; last {
		case '.', ',', ';', ':', '!', '?':
			s = s[:len(s)-1]
		case ')', ']', '}':
			open := map[byte]string{')': "(", ']': "[", '}': "{"}[last]
			if strings.Count(s, open) >= strings.Count(s, string(last)) {
				return s
			}
			s = s[:len(s)-1]
		default:
			return s
		}
	}
	return s
}
