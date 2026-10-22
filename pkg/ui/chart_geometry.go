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
	"fmt"
	"math"
	"strings"
)

// roundCoord rounds an SVG coordinate to 1 decimal place.
func roundCoord(v float64) float64 {
	return math.Round(v*10.0) / 10.0
}

// ChartLine represents a data series line to render in SVG geometry.
type ChartLine struct {
	Color string
	Items []SeriesItem
}

// ChartTick represents an observation tick mark to plot at a commit column.
type ChartTick struct {
	Rate   float64
	Stroke string
}

// ChartGeometryOptions specifies layout and sizing for chart geometry.
type ChartGeometryOptions struct {
	X0, X1, Y0, Y1 float64
	CircleR        float64
	TickHalfHeight float64
	TickStep       float64
}

// ChartGeometryResult holds precomputed SVG elements for inline SVG rendering.
type ChartGeometryResult struct {
	Polylines    []SVGPolyline
	Polygons     []string
	PolygonFills []string
	Circles      []SVGCircle
	Ticks        []SVGObservationTick
	HasPoints    bool
}

// computeChartGeometry generates polylines, shaded CI polygons, circles, and observation ticks
// for one or more lines and optional per-commit observation ticks mapped into [x0, x1] horizontally
// and [y0, y1] vertically (rate 1.0 at y0, 0.0 at y1).
func computeChartGeometry(lines []ChartLine, ticksAtCommit [][]ChartTick, N int, opts ChartGeometryOptions) ChartGeometryResult {
	if N <= 0 {
		return ChartGeometryResult{}
	}

	var polylines []SVGPolyline
	var polygons []string
	var polygonFills []string
	var circles []SVGCircle
	hasPoints := false

	type pt2D struct {
		x float64
		y float64
	}
	type ci2D struct {
		x      float64
		yUpper float64
		yLower float64
	}

	for _, line := range lines {
		var curLine []pt2D
		var curLineStroke string
		var curCI []ci2D

		fillColor := line.Color
		if fillColor == "" {
			fillColor = "var(--indigo)"
		}

		flushLine := func() {
			if len(curLine) >= 2 {
				var sb strings.Builder
				for j, p := range curLine {
					if j > 0 {
						sb.WriteByte(' ')
					}
					sb.WriteString(fmt.Sprintf("%.1f,%.1f", p.x, p.y))
				}
				polylines = append(polylines, SVGPolyline{
					Points: sb.String(),
					Stroke: curLineStroke,
				})
			}
			curLine = nil
			curLineStroke = ""
		}

		flushCI := func() {
			if len(curCI) >= 2 {
				var sb strings.Builder
				for j, p := range curCI {
					if j > 0 {
						sb.WriteByte(' ')
					}
					sb.WriteString(fmt.Sprintf("%.1f,%.1f", p.x, p.yUpper))
				}
				for j := len(curCI) - 1; j >= 0; j-- {
					sb.WriteByte(' ')
					sb.WriteString(fmt.Sprintf("%.1f,%.1f", curCI[j].x, curCI[j].yLower))
				}
				polygons = append(polygons, sb.String())
				polygonFills = append(polygonFills, fillColor)
			} else if len(curCI) == 1 {
				p := curCI[0]
				poly := fmt.Sprintf("%.1f,%.1f %.1f,%.1f %.1f,%.1f %.1f,%.1f",
					roundCoord(p.x-2.0), p.yUpper,
					roundCoord(p.x+2.0), p.yUpper,
					roundCoord(p.x+2.0), p.yLower,
					roundCoord(p.x-2.0), p.yLower,
				)
				polygons = append(polygons, poly)
				polygonFills = append(polygonFills, fillColor)
			}
			curCI = nil
		}

		for i, it := range line.Items {
			if i >= N {
				break
			}
			var cx float64
			if N > 1 {
				cx = opts.X0 + (float64(i)/float64(N-1))*(opts.X1-opts.X0)
			} else {
				cx = (opts.X0 + opts.X1) / 2.0
			}
			cx = roundCoord(cx)

			if !it.HasPoint || it.SuccessRate == nil {
				flushLine()
				flushCI()
				continue
			}

			hasPoints = true
			rate := *it.SuccessRate
			if rate < 0.0 {
				rate = 0.0
			} else if rate > 1.0 {
				rate = 1.0
			}
			cy := roundCoord(opts.Y1 - float64(rate*(opts.Y1-opts.Y0)))

			pt := pt2D{x: cx, y: cy}
			circleFill := "var(--indigo)"
			if line.Color != "" {
				circleFill = line.Color
			}
			circles = append(circles, SVGCircle{CX: cx, CY: cy, R: opts.CircleR, Fill: circleFill})

			if len(curLine) == 0 {
				curLine = append(curLine, pt)
			} else {
				segStroke := "var(--indigo)"
				if line.Color != "" {
					segStroke = line.Color
				} else if it.DeltaPrev != nil {
					if it.DeltaPrev.CI.Lower > 0 {
						segStroke = "var(--good)"
					} else if it.DeltaPrev.CI.Upper < 0 {
						segStroke = "var(--bad)"
					}
				}

				if len(curLine) == 1 {
					curLineStroke = segStroke
					curLine = append(curLine, pt)
				} else if segStroke == curLineStroke {
					curLine = append(curLine, pt)
				} else {
					prevPt := curLine[len(curLine)-1]
					flushLine()
					curLine = []pt2D{prevPt, pt}
					curLineStroke = segStroke
				}
			}

			if it.CI != nil {
				lo := it.CI.Lower
				if lo < 0.0 {
					lo = 0.0
				} else if lo > 1.0 {
					lo = 1.0
				}
				hi := it.CI.Upper
				if hi < 0.0 {
					hi = 0.0
				} else if hi > 1.0 {
					hi = 1.0
				}
				yUpper := roundCoord(opts.Y1 - float64(hi*(opts.Y1-opts.Y0)))
				yLower := roundCoord(opts.Y1 - float64(lo*(opts.Y1-opts.Y0)))
				curCI = append(curCI, ci2D{x: cx, yUpper: yUpper, yLower: yLower})
			} else {
				flushCI()
			}
		}

		flushLine()
		flushCI()
	}

	var ticks []SVGObservationTick
	for i := 0; i < N; i++ {
		var tList []ChartTick
		if i < len(ticksAtCommit) {
			tList = ticksAtCommit[i]
		}
		M := len(tList)
		if M == 0 {
			continue
		}

		var cx float64
		if N > 1 {
			cx = opts.X0 + (float64(i)/float64(N-1))*(opts.X1-opts.X0)
		} else {
			cx = (opts.X0 + opts.X1) / 2.0
		}
		cx = roundCoord(cx)

		step := opts.TickStep
		startX := cx - float64(M-1)*step/2.0
		for j, t := range tList {
			tx := cx
			if M > 1 {
				tx = roundCoord(startX + float64(j)*step)
				if tx < opts.X0 {
					tx = opts.X0
				} else if tx > opts.X1 {
					tx = opts.X1
				}
			}

			rate := t.Rate
			if rate < 0.0 {
				rate = 0.0
			} else if rate > 1.0 {
				rate = 1.0
			}
			cy := roundCoord(opts.Y1 - float64(rate*(opts.Y1-opts.Y0)))
			yTop := roundCoord(math.Max(opts.Y0, cy-opts.TickHalfHeight))
			yBottom := roundCoord(math.Min(opts.Y1, cy+opts.TickHalfHeight))

			stroke := t.Stroke
			if stroke == "" {
				stroke = "var(--indigo)"
			}

			ticks = append(ticks, SVGObservationTick{
				X1:     tx,
				Y1:     yTop,
				X2:     tx,
				Y2:     yBottom,
				Stroke: stroke,
			})
		}
	}

	return ChartGeometryResult{
		Polylines:    polylines,
		Polygons:     polygons,
		PolygonFills: polygonFills,
		Circles:      circles,
		Ticks:        ticks,
		HasPoints:    hasPoints,
	}
}
