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
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/gke-labs/eval-runner/pkg/stats"
	"k8s.io/klog/v2"
)

const (
	// defaultBadgeLabel is the default label for shields badges and endpoints.
	defaultBadgeLabel = ".evals"
	// DefaultSparklineWidth is the standard sparkline width in pixels.
	DefaultSparklineWidth = 120
	// DefaultSparklineHeight is the standard sparkline height in pixels.
	DefaultSparklineHeight = 28
	// MinSparklineWidth is the lower bound on sparkline width.
	MinSparklineWidth = 30
	// MaxSparklineWidth is the upper bound on sparkline width.
	MaxSparklineWidth = 1000
	// MinSparklineHeight is the lower bound on sparkline height.
	MinSparklineHeight = 10
	// MaxSparklineHeight is the upper bound on sparkline height.
	MaxSparklineHeight = 300
)

// BadgeData contains resolved data for rendering a shields-style badge or endpoint.
type BadgeData struct {
	Label       string
	Message     string
	Color       string
	ColorHex    string
	CommitShort string
	Title       string
	IsUnknown   bool
	HasData     bool
}

// ShieldsEndpointResponse conforms to the shields.io endpoint JSON schema.
type ShieldsEndpointResponse struct {
	SchemaVersion int    `json:"schemaVersion"`
	Label         string `json:"label,omitempty"`
	Message       string `json:"message"`
	Color         string `json:"color,omitempty"`
}

// parseDimension parses a dimension query parameter with fallback to defVal and clamping to [minVal, maxVal].
func parseDimension(param string, defVal, minVal, maxVal int) int {
	if param == "" {
		return defVal
	}
	v, err := strconv.Atoi(param)
	if err != nil {
		return defVal
	}
	if v < minVal {
		return minVal
	}
	if v > maxVal {
		return maxVal
	}
	return v
}

// measureTextWidth estimates character width in Verdana 11px scaled by 10 (tenths of a pixel).
func measureTextWidth(s string) int {
	if s == defaultBadgeLabel {
		return int(math.Round(0.8*float64(measureTextWidth(".")))) + measureTextWidth("evals")
	}
	w := 0
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			switch r {
			case 'I':
				w += 40
			case 'M', 'W':
				w += 95
			default:
				w += 75
			}
		case r >= 'a' && r <= 'z':
			switch r {
			case 'i', 'l':
				w += 28
			case 'j', 't', 'f', 'r':
				w += 45
			case 'm', 'w':
				w += 95
			default:
				w += 65
			}
		case r >= '0' && r <= '9':
			w += 68
		case r == ' ' || r == '.':
			w += 35
		case r == '%':
			w += 95
		case r == '±':
			w += 75
		case r == '-' || r == '_' || r == '/':
			w += 45
		default:
			w += 65
		}
	}
	return w
}

// RenderBadgeSVG generates a Shields-style flat badge SVG.
func RenderBadgeSVG(data BadgeData) []byte {
	lbl := data.Label

	labelWidth := (measureTextWidth(lbl) + 120 + 5) / 10
	valWidth := (measureTextWidth(data.Message) + 120 + 5) / 10
	totalWidth := labelWidth + valWidth

	labelCenter10 := (labelWidth * 10) / 2
	valCenter10 := labelWidth*10 + (valWidth*10)/2
	labelTextLength10 := measureTextWidth(lbl)
	valTextLength10 := measureTextWidth(data.Message)

	escLabel := html.EscapeString(lbl)
	escMessage := html.EscapeString(data.Message)
	escTitle := html.EscapeString(data.Title)
	if escTitle == "" {
		escTitle = fmt.Sprintf("%s: %s", escLabel, escMessage)
	}

	colorHex := data.ColorHex
	if colorHex == "" {
		colorHex = "#9f9f9f"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" viewBox="0 0 %d 20" role="img" aria-label="%s: %s">`+"\n", totalWidth, totalWidth, escLabel, escMessage))
	sb.WriteString(fmt.Sprintf(`  <title>%s</title>`+"\n", escTitle))
	sb.WriteString(`  <linearGradient id="s" x2="0" y2="100%">` + "\n")
	sb.WriteString(`    <stop offset="0" stop-color="#bbb" stop-opacity=".1"/>` + "\n")
	sb.WriteString(`    <stop offset="1" stop-opacity=".1"/>` + "\n")
	sb.WriteString(`  </linearGradient>` + "\n")
	sb.WriteString(`  <clipPath id="r">` + "\n")
	sb.WriteString(fmt.Sprintf(`    <rect width="%d" height="20" rx="3" fill="#fff"/>`+"\n", totalWidth))
	sb.WriteString(`  </clipPath>` + "\n")
	sb.WriteString(`  <g clip-path="url(#r)">` + "\n")
	sb.WriteString(fmt.Sprintf(`    <rect width="%d" height="20" fill="#555"/>`+"\n", labelWidth))
	sb.WriteString(fmt.Sprintf(`    <rect x="%d" width="%d" height="20" fill="%s"/>`+"\n", labelWidth, valWidth, colorHex))
	sb.WriteString(fmt.Sprintf(`    <rect width="%d" height="20" fill="url(#s)"/>`+"\n", totalWidth))
	sb.WriteString(`  </g>` + "\n")
	sb.WriteString(`  <g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" text-rendering="geometricPrecision" font-size="110">` + "\n")
	if lbl == defaultBadgeLabel {
		labelLeft := (labelWidth*10 - labelTextLength10) / 2
		sb.WriteString(fmt.Sprintf(`    <text aria-hidden="true" x="%d" y="150" fill="#010101" fill-opacity=".3" transform="scale(.1)" text-anchor="start" font-size="88" textLength="32">.</text>`+"\n", labelLeft))
		sb.WriteString(fmt.Sprintf(`    <text aria-hidden="true" x="%d" y="150" fill="#010101" fill-opacity=".3" transform="scale(.1)" text-anchor="start" textLength="288">evals</text>`+"\n", labelLeft+35))
		sb.WriteString(fmt.Sprintf(`    <text x="%d" y="140" transform="scale(.1)" fill="#fff" fill-opacity=".8" text-anchor="start" font-size="88" textLength="32">.</text>`+"\n", labelLeft))
		sb.WriteString(fmt.Sprintf(`    <text x="%d" y="140" transform="scale(.1)" fill="#fff" text-anchor="start" textLength="288">evals</text>`+"\n", labelLeft+35))
	} else {
		sb.WriteString(fmt.Sprintf(`    <text aria-hidden="true" x="%d" y="150" fill="#010101" fill-opacity=".3" transform="scale(.1)" textLength="%d">%s</text>`+"\n", labelCenter10, labelTextLength10, escLabel))
		sb.WriteString(fmt.Sprintf(`    <text x="%d" y="140" transform="scale(.1)" fill="#fff" textLength="%d">%s</text>`+"\n", labelCenter10, labelTextLength10, escLabel))
	}
	sb.WriteString(fmt.Sprintf(`    <text aria-hidden="true" x="%d" y="150" fill="#010101" fill-opacity=".3" transform="scale(.1)" textLength="%d">%s</text>`+"\n", valCenter10, valTextLength10, escMessage))
	sb.WriteString(fmt.Sprintf(`    <text x="%d" y="140" transform="scale(.1)" fill="#fff" textLength="%d">%s</text>`+"\n", valCenter10, valTextLength10, escMessage))
	sb.WriteString(`  </g>` + "\n")
	sb.WriteString(`</svg>` + "\n")

	return []byte(sb.String())
}

// RenderStandaloneSparklineSVG generates a standalone SVG sparkline with explicit attributes.
func RenderStandaloneSparklineSVG(spark SparklineSVG, w, h int, title string) []byte {
	escTitle := html.EscapeString(title)
	viewBox := spark.ViewBox
	if viewBox == "" {
		viewBox = fmt.Sprintf("0 0 %d %d", w, h)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="%s" role="img">`+"\n", w, h, viewBox))
	if escTitle != "" {
		sb.WriteString(fmt.Sprintf(`  <title>%s</title>`+"\n", escTitle))
	}

	if !spark.HasPoints {
		msg := title
		if msg == "" {
			msg = "no data"
		}
		escMsg := html.EscapeString(msg)
		fontSize := 10
		if h < 20 {
			fontSize = 8
		}
		sb.WriteString(fmt.Sprintf(`  <text x="%d" y="%d" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" font-size="%d" fill="#9f9f9f">%s</text>`+"\n", w/2, h/2+fontSize/2-1, fontSize, escMsg))
	} else {
		for _, poly := range spark.Polygons {
			sb.WriteString(fmt.Sprintf(`  <polygon points="%s" fill="#4F46E5" fill-opacity="0.15" stroke="none"/>`+"\n", poly))
		}
		for _, pl := range spark.Polylines {
			stroke := pl.Stroke
			if stroke == "" || stroke == "var(--indigo)" {
				stroke = "#4F46E5"
			}
			sb.WriteString(fmt.Sprintf(`  <polyline points="%s" fill="none" stroke="%s" stroke-width="1.5"/>`+"\n", pl.Points, stroke))
		}
		for _, c := range spark.Circles {
			sb.WriteString(fmt.Sprintf(`  <circle cx="%.1f" cy="%.1f" r="%.1f" fill="#4F46E5"/>`+"\n", c.CX, c.CY, c.R))
		}
		for _, tick := range spark.Ticks {
			stroke := tick.Stroke
			if stroke == "" || stroke == "var(--indigo)" {
				stroke = "#4F46E5"
			}
			sb.WriteString(fmt.Sprintf(`  <line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="1.5" stroke-linecap="round"/>`+"\n", tick.X1, tick.Y1, tick.X2, tick.Y2, stroke))
		}
	}

	sb.WriteString(`</svg>` + "\n")
	return []byte(sb.String())
}

// writeCachedResponse writes HTTP headers and body with caching headers (ETag, Cache-Control) and 304 support.
func writeCachedResponse(w http.ResponseWriter, r *http.Request, statusCode int, contentType string, body []byte) {
	etag := fmt.Sprintf(`"%x"`, sha256.Sum256(body))
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("ETag", etag)
	if match := r.Header.Get("If-None-Match"); match != "" {
		if match == etag || match == fmt.Sprintf(`W/%s`, etag) || strings.Contains(match, etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.WriteHeader(statusCode)
	if _, err := w.Write(body); err != nil {
		klog.Warningf("failed to write cached response: %v", err)
	}
}

func (s *Server) writeBadgeError(w http.ResponseWriter, logMsg string, err error) {
	if err != nil {
		klog.Warningf("%s: %v", logMsg, err)
	} else {
		klog.Warning(logMsg)
	}
	body := RenderBadgeSVG(BadgeData{
		Label:    defaultBadgeLabel,
		Message:  "error",
		Color:    "grey",
		ColorHex: "#9f9f9f",
		Title:    "error",
	})
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(http.StatusInternalServerError)
	if _, wErr := w.Write(body); wErr != nil {
		klog.Warningf("failed to write badge error: %v", wErr)
	}
}

func (s *Server) writeSparklineError(w http.ResponseWriter, wPx, hPx int, logMsg string, err error) {
	if err != nil {
		klog.Warningf("%s: %v", logMsg, err)
	} else {
		klog.Warning(logMsg)
	}
	body := RenderStandaloneSparklineSVG(SparklineSVG{ViewBox: fmt.Sprintf("0 0 %d %d", wPx, hPx)}, wPx, hPx, "error")
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(http.StatusInternalServerError)
	if _, wErr := w.Write(body); wErr != nil {
		klog.Warningf("failed to write sparkline error: %v", wErr)
	}
}

func (s *Server) writeBadgeJSONError(w http.ResponseWriter, logMsg string, err error) {
	if err != nil {
		klog.Warningf("%s: %v", logMsg, err)
	} else {
		klog.Warning(logMsg)
	}
	resp := ShieldsEndpointResponse{
		SchemaVersion: 1,
		Label:         defaultBadgeLabel,
		Message:       "error",
		Color:         "grey",
	}
	body, mErr := json.Marshal(resp)
	if mErr != nil {
		body = []byte(`{"schemaVersion":1,"label":".evals","message":"error","color":"grey"}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(http.StatusInternalServerError)
	if _, wErr := w.Write(body); wErr != nil {
		klog.Warningf("failed to write badge json error: %v", wErr)
	}
}

// resolveRepoBadgeData gathers observations and computes badge metrics for the repository.
func (s *Server) resolveRepoBadgeData(ctx context.Context, r *http.Request, ref RepoRef, ns string) (*BadgeData, *RepoObservations, []SeriesItem, int, error) {
	evalID := strings.TrimSpace(r.URL.Query().Get("eval"))
	if len(evalID) > 40 {
		evalID = evalID[:37] + "..."
	}
	label := defaultBadgeLabel
	if evalID != "" {
		label = evalID
	}

	branch := strings.TrimSpace(r.URL.Query().Get("branch"))
	if branch == "" {
		branch = "main"
	}
	cohortFilter := strings.TrimSpace(r.URL.Query().Get("cohort"))

	obs, err := s.collectRepoObservations(ctx, ns, ref, branch, cohortFilter)
	if err != nil {
		return nil, nil, nil, http.StatusInternalServerError, err
	}

	if obs.TotalRuns == 0 {
		return &BadgeData{
			Label:     label,
			Message:   "unknown",
			Color:     "grey",
			ColorHex:  "#9f9f9f",
			Title:     "unknown",
			IsUnknown: true,
		}, nil, nil, http.StatusNotFound, nil
	}

	var (
		hasData   bool
		est       stats.Estimate
		commitSHA string
	)

	if evalID != "" {
		var evalCommits []CommitEntry
		for _, c := range obs.Commits {
			if len(obs.ByCommit[evalID][c.SHA]) > 0 {
				evalCommits = append(evalCommits, c)
			}
		}
		if len(evalCommits) > 0 {
			newestCommit := evalCommits[len(evalCommits)-1]
			newestObs := obs.ByCommit[evalID][newestCommit.SHA]
			var newestTasks []stats.TaskCounts
			for _, o := range newestObs {
				newestTasks = append(newestTasks, stats.TaskCounts{
					Successes: o.Successes,
					Samples:   o.Samples,
				})
			}
			est = stats.PooledWilson(newestTasks, 0.95)
			commitSHA = newestCommit.SHA
			hasData = true
		}
	} else {
		scores := suiteScores(obs)
		if len(scores.SortedSuites) > 0 && scores.OverallEst.N > 0 {
			est = scores.OverallEst
			commitSHA = scores.CommitSHA
			hasData = true
		}
	}

	badge := &BadgeData{
		Label:   label,
		HasData: hasData,
	}

	if !hasData {
		badge.Message = "no data"
		badge.Color = "grey"
		badge.ColorHex = "#9f9f9f"
		badge.Title = "no data"
	} else {
		pct := int(math.Round(est.Point * 100))
		halfWidth := int(math.Round(((est.CI.Upper - est.CI.Lower) / 2.0) * 100))
		badge.Message = fmt.Sprintf("%d%% ± %d", pct, halfWidth)
		if pct < 50 {
			badge.Color = "red"
			badge.ColorHex = "#e05d44"
		} else if pct < 80 {
			badge.Color = "yellow"
			badge.ColorHex = "#dfb317"
		} else {
			badge.Color = "green"
			badge.ColorHex = "#4c1"
		}
		badge.CommitShort = ShortCommit(commitSHA)
		if badge.CommitShort != "" {
			badge.Title = fmt.Sprintf("%s (%s)", badge.Message, badge.CommitShort)
		} else {
			badge.Title = badge.Message
		}
	}

	// Sparkline items: up to last 20 commits
	sparklineCommits := obs.Commits
	if len(sparklineCommits) > 20 {
		sparklineCommits = sparklineCommits[len(sparklineCommits)-20:]
	}

	var sparkItems []SeriesItem
	if evalID != "" {
		sparkItems = obs.EvalSparklineItems(evalID, sparklineCommits)
	} else {
		sparkItems = obs.OverallSparklineItems(sparklineCommits)
	}

	return badge, obs, sparkItems, http.StatusOK, nil
}

func (s *Server) handleRepoBadgeSVG(w http.ResponseWriter, r *http.Request) {
	ref, ok := s.parseRouteRepo(r)
	if !ok {
		body := RenderBadgeSVG(BadgeData{
			Label:    defaultBadgeLabel,
			Message:  "unknown",
			Color:    "grey",
			ColorHex: "#9f9f9f",
			Title:    "unknown",
		})
		writeCachedResponse(w, r, http.StatusNotFound, "image/svg+xml", body)
		return
	}

	ns, err := s.namespaceFor(r)
	if err != nil {
		s.writeBadgeError(w, "invalid namespace query param", err)
		return
	}

	data, _, _, status, err := s.resolveRepoBadgeData(r.Context(), r, ref, ns)
	if err != nil {
		s.writeBadgeError(w, "failed to collect badge data", err)
		return
	}

	body := RenderBadgeSVG(*data)
	writeCachedResponse(w, r, status, "image/svg+xml", body)
}

func (s *Server) handleRepoSparklineSVG(w http.ResponseWriter, r *http.Request) {
	wPx := parseDimension(r.URL.Query().Get("w"), DefaultSparklineWidth, MinSparklineWidth, MaxSparklineWidth)
	hPx := parseDimension(r.URL.Query().Get("h"), DefaultSparklineHeight, MinSparklineHeight, MaxSparklineHeight)

	ref, ok := s.parseRouteRepo(r)
	if !ok {
		body := RenderStandaloneSparklineSVG(SparklineSVG{ViewBox: fmt.Sprintf("0 0 %d %d", wPx, hPx)}, wPx, hPx, "unknown")
		writeCachedResponse(w, r, http.StatusNotFound, "image/svg+xml", body)
		return
	}

	ns, err := s.namespaceFor(r)
	if err != nil {
		s.writeSparklineError(w, wPx, hPx, "invalid namespace query param", err)
		return
	}

	data, _, sparkItems, status, err := s.resolveRepoBadgeData(r.Context(), r, ref, ns)
	if err != nil {
		s.writeSparklineError(w, wPx, hPx, "failed to collect sparkline data", err)
		return
	}

	if status == http.StatusNotFound {
		body := RenderStandaloneSparklineSVG(SparklineSVG{ViewBox: fmt.Sprintf("0 0 %d %d", wPx, hPx)}, wPx, hPx, "unknown")
		writeCachedResponse(w, r, http.StatusNotFound, "image/svg+xml", body)
		return
	}

	spark := BuildSparklineSVGWithOptions(sparkItems, wPx, hPx)
	title := data.Title
	if !spark.HasPoints {
		title = "no data"
	}
	body := RenderStandaloneSparklineSVG(spark, wPx, hPx, title)
	writeCachedResponse(w, r, http.StatusOK, "image/svg+xml", body)
}

func (s *Server) handleRepoBadgeJSON(w http.ResponseWriter, r *http.Request) {
	ref, ok := s.parseRouteRepo(r)
	if !ok {
		resp := ShieldsEndpointResponse{
			SchemaVersion: 1,
			Label:         defaultBadgeLabel,
			Message:       "unknown",
			Color:         "grey",
		}
		body, err := json.Marshal(resp)
		if err != nil {
			s.writeBadgeJSONError(w, "failed to marshal badge json", err)
			return
		}
		writeCachedResponse(w, r, http.StatusNotFound, "application/json; charset=utf-8", body)
		return
	}

	ns, err := s.namespaceFor(r)
	if err != nil {
		s.writeBadgeJSONError(w, "invalid namespace query param", err)
		return
	}

	data, _, _, status, err := s.resolveRepoBadgeData(r.Context(), r, ref, ns)
	if err != nil {
		s.writeBadgeJSONError(w, "failed to collect badge data", err)
		return
	}

	resp := ShieldsEndpointResponse{
		SchemaVersion: 1,
		Label:         data.Label,
		Message:       data.Message,
		Color:         data.Color,
	}
	body, err := json.Marshal(resp)
	if err != nil {
		s.writeBadgeJSONError(w, "failed to marshal badge json", err)
		return
	}
	writeCachedResponse(w, r, status, "application/json; charset=utf-8", body)
}
