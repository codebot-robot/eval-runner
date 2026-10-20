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
	"html/template"
	"io"
)

const baseCSS = `
:root {
  --indigo: #4F46E5;
  --indigo-soft: #EEF2FF;
  --teal: #14B8A6;
  --teal-soft: #CCFBF1;
  --ink: #111827;
  --ink-2: #374151;
  --muted: #6B7280;
  --line: #E5E7EB;
  --surface: #FFFFFF;
  --canvas: #F8FAFC;
  --good: #15803D;
  --good-soft: #DCFCE7;
  --bad: #B91C1C;
  --bad-soft: #FEE2E2;
  --warn: #B45309;
  --warn-soft: #FEF3C7;
  --neutral: #6B7280;
  --neutral-soft: #F3F4F6;
  --sans: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  --mono: "SF Mono", Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace;
}

* { box-sizing: border-box; }
body {
  margin: 0;
  padding: 0;
  font-family: var(--sans);
  color: var(--ink);
  background: var(--canvas);
  -webkit-font-smoothing: antialiased;
  min-height: 100vh;
}
a { color: var(--indigo); text-decoration: none; }
a:hover { text-decoration: underline; }

.header-bar {
  background: var(--surface);
  border-bottom: 1px solid var(--line);
  padding: 14px 28px;
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.header-brand {
  display: flex;
  align-items: center;
  gap: 24px;
}
.header-logo {
  font-weight: 700;
  font-size: 20px;
  color: var(--indigo);
  text-decoration: none;
  display: flex;
  align-items: center;
  gap: 8px;
}
.header-logo:hover { text-decoration: none; }
.nav-links {
  display: flex;
  gap: 8px;
}
.nav-link {
  font-size: 14px;
  font-weight: 500;
  color: var(--muted);
  padding: 6px 14px;
  border-radius: 6px;
  text-decoration: none;
  transition: all 0.15s ease;
}
.nav-link:hover {
  color: var(--ink);
  background: var(--neutral-soft);
  text-decoration: none;
}
.nav-link.active {
  color: var(--indigo);
  background: var(--indigo-soft);
  font-weight: 600;
}

.container {
  max-width: 1320px;
  margin: 28px auto;
  padding: 0 24px;
}

.card {
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: 12px;
  box-shadow: 0 2px 6px rgba(17,24,39,.04);
  overflow: hidden;
  margin-bottom: 24px;
}
.card-header {
  padding: 18px 24px;
  border-bottom: 1px solid var(--line);
  background: var(--surface);
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 12px;
}
.card-title {
  font-size: 20px;
  font-weight: 600;
  margin: 0;
  display: flex;
  align-items: center;
  gap: 12px;
}
.card-body {
  padding: 24px;
}
.card-footer {
  padding: 16px 24px;
  background: var(--canvas);
  border-top: 1px solid var(--line);
  color: var(--muted);
  font-size: 14px;
  display: flex;
  flex-wrap: wrap;
  gap: 28px;
  align-items: center;
}

.mono { font-family: var(--mono); }
.num { font-family: var(--mono); font-variant-numeric: tabular-nums; }

.chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 3px 10px;
  border-radius: 999px;
  font-size: 13px;
  font-weight: 600;
  line-height: 1.2;
}
.chip.good { background: var(--good-soft); color: var(--good); }
.chip.bad { background: var(--bad-soft); color: var(--bad); }
.chip.warn { background: var(--warn-soft); color: var(--warn); }
.chip.neutral { background: var(--neutral-soft); color: var(--neutral); }
.chip.pending { background: var(--warn-soft); color: var(--warn); }
.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: currentColor;
  display: inline-block;
}

/* Confidence-interval bar against zero line (Comparison) */
.cibar { position: relative; height: 26px; width: 340px; }
.cibar .axis { position: absolute; left: 0; right: 0; top: 12px; height: 2px; background: var(--line); }
.cibar .zero { position: absolute; top: 2px; width: 2px; height: 22px; background: var(--muted); }
.cibar .range { position: absolute; top: 11px; height: 4px; border-radius: 2px; }
.cibar .pt { position: absolute; top: 6px; width: 14px; height: 14px; border-radius: 50%; border: 2px solid white; box-sizing: border-box; }
.good-c { background: var(--good); }
.bad-c { background: var(--bad); }
.warn-c { background: var(--warn); }
.neutral-c { background: var(--neutral); }
.indigo-c { background: var(--indigo); }
.teal-c { background: var(--teal); }

/* Confidence-interval bar against 0-1 axis (Run) */
.cibar-01 { position: relative; height: 26px; width: 240px; }
.cibar-01 .axis { position: absolute; left: 0; right: 0; top: 12px; height: 2px; background: var(--line); }
.cibar-01 .tick-0 { position: absolute; left: 0; top: 5px; width: 1px; height: 15px; background: var(--muted); }
.cibar-01 .tick-50 { position: absolute; left: 120px; top: 8px; width: 1px; height: 10px; background: var(--line); }
.cibar-01 .tick-100 { position: absolute; right: 0; top: 5px; width: 1px; height: 15px; background: var(--muted); }
.cibar-01 .range { position: absolute; top: 11px; height: 4px; border-radius: 2px; background: var(--indigo); }
.cibar-01 .pt { position: absolute; top: 6px; width: 14px; height: 14px; border-radius: 50%; border: 2px solid white; box-sizing: border-box; background: var(--indigo); }

/* Live Task Progress */
.progress-container { display: flex; align-items: center; gap: 8px; }
.progress-bar { position: relative; height: 8px; width: 140px; background: var(--line); border-radius: 999px; overflow: hidden; }
.progress-fill { height: 100%; background: var(--teal); border-radius: 999px; }
.progress-text { font-size: 12px; color: var(--muted); font-family: var(--mono); white-space: nowrap; }

table.ci { width: 100%; border-collapse: collapse; font-size: 15px; }
table.ci th { text-align: left; color: var(--muted); font-weight: 500; font-size: 13px; padding: 10px 14px; border-bottom: 1px solid var(--line); }
table.ci td { padding: 12px 14px; border-bottom: 1px solid var(--line); vertical-align: middle; }
table.ci tbody tr:hover { background: #FAFBFD; }
table.ci tbody tr:last-child td { border-bottom: none; }

.verdict { font-size: 20px; margin: 4px 0 20px; display: flex; align-items: center; gap: 14px; flex-wrap: wrap; }
.verdict-headline { font-size: 17px; color: var(--ink-2); }

.meta-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 14px; margin-bottom: 22px; }
.meta-tile { border: 1px solid var(--line); border-radius: 8px; padding: 12px 16px; background: var(--canvas); }
.meta-tile .label { font-size: 11px; color: var(--muted); text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 4px; }
.meta-tile .value { font-size: 14px; font-weight: 600; color: var(--ink); word-break: break-all; }

.empty-state { text-align: center; padding: 48px 16px; color: var(--muted); font-size: 15px; }
.log-path { font-size: 12px; background: var(--neutral-soft); padding: 2px 6px; border-radius: 4px; display: inline-block; max-width: 320px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
a.log-path { color: var(--indigo); text-decoration: none; }
a.log-path:hover { text-decoration: underline; background: var(--indigo-soft); }
.msg-alert { margin-top: 14px; padding: 10px 14px; background: var(--bad-soft); color: var(--bad); border-radius: 6px; font-size: 14px; }
`

const runsListTemplate = `
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Runs · eval-runner</title>
  <style>` + baseCSS + `</style>
</head>
<body>
  <header class="header-bar">
    <div class="header-brand">
      <a href="/runs" class="header-logo">eval-runner</a>
      <nav class="nav-links">
        <a href="/pulls" class="nav-link {{ if eq .ActiveTab "pulls" }}active{{ end }}">Pull Requests</a>
        <a href="/runs" class="nav-link {{ if eq .ActiveTab "runs" }}active{{ end }}">Runs</a>
        <a href="/comparisons" class="nav-link {{ if eq .ActiveTab "comparisons" }}active{{ end }}">Comparisons</a>
      </nav>
    </div>
    <div style="font-size:13px;color:var(--muted)">namespace: <span class="mono">{{ .Namespace }}</span></div>
  </header>

  <main class="container">
    <div class="card">
      <div class="card-header">
        <h1 class="card-title">Runs</h1>
        <span style="font-size:13px;color:var(--muted)">{{ .TotalCount }} total</span>
      </div>
      <div style="overflow-x:auto">
        <table class="ci">
          <thead>
            <tr>
              <th>Run</th>
              <th>Namespace</th>
              <th>Phase</th>
              <th>Tasks</th>
              <th>Samples</th>
              <th>Rates</th>
              <th>Commit</th>
              <th>Age</th>
            </tr>
          </thead>
          <tbody>
            {{ range .Runs }}
            <tr>
              <td><a href="{{ .Link }}" class="mono" style="font-weight:600">{{ .Name }}</a></td>
              <td><span class="mono" style="font-size:13px">{{ .Namespace }}</span></td>
              <td><span class="chip {{ .PhaseChip }}"><span class="dot"></span>{{ .Phase }}</span></td>
              <td class="num">{{ .Tasks }}</td>
              <td class="num">{{ if .SampleRange }}{{ .SampleRange }}{{ else }}{{ .Samples }}{{ end }}</td>
              <td class="mono" style="font-size:13px">{{ if .Rates }}{{ .Rates }}{{ else }}—{{ end }}</td>
              <td><span class="mono" style="font-size:13px">{{ if .Commit }}{{ .Commit }}{{ else }}—{{ end }}</span></td>
              <td style="color:var(--muted);font-size:13px">{{ .Age }}</td>
            </tr>
            {{ else }}
            <tr>
              <td colspan="8" class="empty-state">No Runs found in namespace {{ .Namespace }}.</td>
            </tr>
            {{ end }}
          </tbody>
        </table>
      </div>
      {{ if or .HasPrev .HasMore }}
      <div class="pagination" style="padding:14px 24px;border-top:1px solid var(--line);display:flex;justify-content:space-between;align-items:center;font-size:13px">
        <div>
          {{ if .HasPrev }}<a href="{{ .PrevLink }}">← Previous</a>{{ end }}
        </div>
        <div style="color:var(--muted)">{{ .ShowingRange }}</div>
        <div>
          {{ if .HasMore }}<a href="{{ .NextLink }}">More →</a>{{ end }}
        </div>
      </div>
      {{ end }}
    </div>
  </main>
</body>
</html>
`

const runDetailTemplate = `
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{ .Name }} · Run · eval-runner</title>
  {{- if eq .Phase "Running" }}
  <meta http-equiv="refresh" content="15">
  {{- end }}
  <style>` + baseCSS + `</style>
</head>
<body>
  <header class="header-bar">
    <div class="header-brand">
      <a href="/runs" class="header-logo">eval-runner</a>
      <nav class="nav-links">
        <a href="/pulls" class="nav-link {{ if eq .ActiveTab "pulls" }}active{{ end }}">Pull Requests</a>
        <a href="/runs" class="nav-link {{ if eq .ActiveTab "runs" }}active{{ end }}">Runs</a>
        <a href="/comparisons" class="nav-link {{ if eq .ActiveTab "comparisons" }}active{{ end }}">Comparisons</a>
      </nav>
    </div>
    <div style="font-size:13px;color:var(--muted)">namespace: <span class="mono">{{ .Namespace }}</span></div>
  </header>

  <main class="container">
    <div class="card">
      <div class="card-header">
        <h1 class="card-title">
          <span class="mono">{{ .Name }}</span>
          <span class="chip {{ .PhaseChip }}"><span class="dot"></span>{{ .Phase }}</span>
        </h1>
        <div style="font-size:13px;color:var(--muted)"><a href="/runs">← Back to Runs</a></div>
      </div>
      <div class="card-body">
        <div class="meta-grid">
          <div class="meta-tile">
            <div class="label">Repository</div>
            <div class="value">{{ if .RepoURL }}{{ .RepoURL }}{{ else }}—{{ end }}</div>
          </div>
          <div class="meta-tile">
            <div class="label">Commit</div>
            <div class="value mono">{{ if .CommitShort }}{{ .CommitShort }}{{ else }}—{{ end }}</div>
          </div>
          <div class="meta-tile">
            <div class="label">Model</div>
            <div class="value mono">{{ if .Model }}{{ .Model }}{{ else }}—{{ end }}</div>
          </div>
          <div class="meta-tile">
            <div class="label">Age</div>
            <div class="value">{{ .Age }}</div>
          </div>
        </div>

        <div class="meta-grid">
          <div class="meta-tile">
            <div class="label">Tasks</div>
            <div class="value">{{ if .TasksSummary }}{{ .TasksSummary }}{{ else }}0/0{{ end }} ({{ .TasksCompleted }} ok, {{ .TasksFailed }} fail)</div>
          </div>
          <div class="meta-tile">
            <div class="label">Total Samples</div>
            <div class="value num">{{ .TotalSamples }}{{ if .SampleRange }} <span style="font-size:13px;font-weight:normal;color:var(--muted)">({{ .SampleRange }})</span>{{ end }}</div>
          </div>
          <div class="meta-tile">
            <div class="label">Mean Success Rate</div>
            <div class="value">{{ if .MeanSuccessRate }}{{ .MeanSuccessRate }}{{ else }}—{{ end }}</div>
          </div>
          <div class="meta-tile">
            <div class="label">Artifacts</div>
            <div class="value mono" style="font-size:12px">{{ if .ArtifactsPrefix }}{{ if .ArtifactsURL }}<a class="log-path mono" href="{{ .ArtifactsURL }}" target="_blank" rel="noopener noreferrer" title="{{ .ArtifactsPrefix }}">{{ .ArtifactsPrefix }}</a>{{ else }}<span class="log-path" title="{{ .ArtifactsPrefix }}">{{ .ArtifactsPrefix }}</span>{{ end }}{{ else }}—{{ end }}</div>
          </div>
        </div>

        {{ if .Message }}
        <div class="msg-alert"><b>Message:</b> {{ .Message }}</div>
        {{ end }}

        <h2 style="font-size:17px;font-weight:600;margin:24px 0 12px">Eval Tasks</h2>
        <div style="overflow-x:auto">
          <table class="ci">
            <thead>
              <tr>
                <th>eval</th>
                <th>phase</th>
                <th>rate</th>
                <th colspan="2">95% CI (0-1 axis)</th>
                <th>n</th>
                <th>method</th>
                <th>log path</th>
              </tr>
            </thead>
            <tbody>
              {{ range .Tasks }}
              <tr>
                <td class="mono" style="font-weight:600">{{ .ID }}</td>
                <td><span class="chip {{ .PhaseChip }}"><span class="dot"></span>{{ .Phase }}</span></td>
                <td class="num">{{ .RateStr }}</td>
                <td class="num" style="font-size:13px">{{ if .HasCI }}{{ .CILabel }}{{ else }}—{{ end }}</td>
                <td>
                  {{ if .HasCI }}
                  <div class="cibar-01" title="Rate: {{ .RateStr }} {{ .CILabel }}">
                    <div class="axis"></div>
                    <div class="tick-0"></div>
                    <div class="tick-50"></div>
                    <div class="tick-100"></div>
                    <div class="range" style="left:{{ printf "%.1f" .CILeftPx }}px; width:{{ printf "%.1f" .CIWidthPx }}px"></div>
                    <div class="pt" style="left:{{ printf "%.1f" .CIDotPx }}px"></div>
                  </div>
                  {{ else if eq .Phase "Running" }}
                  <div class="progress-container">
                    <div class="progress-bar" title="{{ .ProgressText }}">
                      <div class="progress-fill" style="width:{{ printf "%.1f" .ProgressPct }}%"></div>
                    </div>
                    <span class="progress-text">{{ .ProgressText }}</span>
                  </div>
                  {{ else }}
                  <span style="color:var(--muted)">—</span>
                  {{ end }}
                </td>
                <td class="num">{{ if gt .Samples 0 }}{{ .Successes }}/{{ .Samples }}{{ if .SampleRange }} <span style="font-size:11px;color:var(--muted)">({{ .SampleRange }})</span>{{ end }}{{ else }}—{{ end }}</td>
                <td class="mono" style="font-size:13px">{{ if .Method }}{{ .Method }}{{ else }}—{{ end }}</td>
                <td>
                  {{ if .LogPath }}
                  {{ if .SamplesLink }}
                  <a href="{{ .SamplesLink }}" class="log-path mono" title="{{ .LogPath }}">{{ .LogPath }}</a>
                  {{ else }}
                  <span class="log-path mono" title="{{ .LogPath }}">{{ .LogPath }}</span>
                  {{ end }}
                  {{ if .LogURL }}<a href="{{ .LogURL }}" target="_blank" rel="noopener noreferrer" title="open in Cloud Storage" style="margin-left:4px">&#8599;</a>{{ end }}
                  {{ else }}
                  <span style="color:var(--muted)">—</span>
                  {{ end }}
                </td>
              </tr>
              {{ if .Message }}
              <tr>
                <td colspan="8" style="padding-top:0;padding-bottom:12px;color:var(--muted);font-size:12px">
                  <b>Detail:</b> {{ .Message }}
                </td>
              </tr>
              {{ end }}
              {{ else }}
              <tr>
                <td colspan="8" class="empty-state">No task executions recorded for this run.</td>
              </tr>
              {{ end }}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </main>
</body>
</html>
`

const comparisonsListTemplate = `
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Comparisons · eval-runner</title>
  <style>` + baseCSS + `</style>
</head>
<body>
  <header class="header-bar">
    <div class="header-brand">
      <a href="/runs" class="header-logo">eval-runner</a>
      <nav class="nav-links">
        <a href="/pulls" class="nav-link {{ if eq .ActiveTab "pulls" }}active{{ end }}">Pull Requests</a>
        <a href="/runs" class="nav-link {{ if eq .ActiveTab "runs" }}active{{ end }}">Runs</a>
        <a href="/comparisons" class="nav-link {{ if eq .ActiveTab "comparisons" }}active{{ end }}">Comparisons</a>
      </nav>
    </div>
    <div style="font-size:13px;color:var(--muted)">namespace: <span class="mono">{{ .Namespace }}</span></div>
  </header>

  <main class="container">
    <div class="card">
      <div class="card-header">
        <h1 class="card-title">Comparisons</h1>
        <span style="font-size:13px;color:var(--muted)">{{ .TotalCount }} total</span>
      </div>
      <div style="overflow-x:auto">
        <table class="ci">
          <thead>
            <tr>
              <th>Comparison</th>
              <th>Namespace</th>
              <th>Verdict</th>
              <th>P(imp)</th>
              <th>Tasks</th>
              <th>Baseline Run</th>
              <th>Candidate Run</th>
              <th>Age</th>
            </tr>
          </thead>
          <tbody>
            {{ range .Comparisons }}
            <tr>
              <td><a href="{{ .Link }}" class="mono" style="font-weight:600">{{ .Name }}</a></td>
              <td><span class="mono" style="font-size:13px">{{ .Namespace }}</span></td>
              <td><span class="chip {{ .VerdictChip }}"><span class="dot"></span>{{ .Verdict }}</span></td>
              <td class="num">{{ .PImprovement }}</td>
              <td class="num">{{ .Tasks }}</td>
              <td><a href="/runs/{{ .Namespace }}/{{ .BaselineRun }}" class="mono" style="font-size:13px">{{ .BaselineRun }}</a></td>
              <td><a href="/runs/{{ .Namespace }}/{{ .CandidateRun }}" class="mono" style="font-size:13px">{{ .CandidateRun }}</a></td>
              <td style="color:var(--muted);font-size:13px">{{ .Age }}</td>
            </tr>
            {{ else }}
            <tr>
              <td colspan="8" class="empty-state">No Comparisons found in namespace {{ .Namespace }}.</td>
            </tr>
            {{ end }}
          </tbody>
        </table>
      </div>
      {{ if or .HasPrev .HasMore }}
      <div class="pagination" style="padding:14px 24px;border-top:1px solid var(--line);display:flex;justify-content:space-between;align-items:center;font-size:13px">
        <div>
          {{ if .HasPrev }}<a href="{{ .PrevLink }}">← Previous</a>{{ end }}
        </div>
        <div style="color:var(--muted)">{{ .ShowingRange }}</div>
        <div>
          {{ if .HasMore }}<a href="{{ .NextLink }}">More →</a>{{ end }}
        </div>
      </div>
      {{ end }}
    </div>
  </main>
</body>
</html>
`

const ComparisonTableTemplate = `{{ define "comparison_table" }}
<div style="overflow-x:auto;margin-top:16px">
  <table class="ci">
    <thead>
      <tr>
        <th>eval</th>{{ if .HasAffinity }}
        <th>affinity</th>{{ end }}
        <th>base</th>
        <th>this PR</th>
        <th colspan="2">Δ (95% CI)</th>
        <th>label</th>
        <th>samples</th>{{ if .HasTokens }}
        <th>tokens</th>{{ end }}
      </tr>
    </thead>
    <tbody>
      {{ range .Tasks }}
      <tr>
        <td class="mono" style="font-weight:600">{{ .ID }}</td>{{ if $.HasAffinity }}
        <td class="mono">{{ .Affinity }}</td>{{ end }}
        <td class="num">{{ .BaseRate }}</td>
        <td class="num">{{ .CandRate }}</td>
        <td class="num">{{ .Delta }}{{ if .HasCI }} <span style="font-size:13px;color:var(--muted)">{{ .CILabel }}</span>{{ end }}</td>
        <td>
          {{ if .HasCI }}
          <div class="cibar" title="Δ: {{ .Delta }} {{ .CILabel }}">
            <div class="axis"></div>
            <div class="zero" style="left:170px"></div>
            <div class="range {{ .StatusClass }}-c" style="left:{{ printf "%.1f" .CILeftPx }}px; width:{{ printf "%.1f" .CIWidthPx }}px"></div>
            <div class="pt {{ .StatusClass }}-c" style="left:{{ printf "%.1f" .CIDotPx }}px"></div>
          </div>
          {{ else }}
          <span style="color:var(--muted)">—</span>
          {{ end }}
        </td>
        <td><span class="chip {{ .StatusClass }}"><span class="dot"></span>{{ .Label }}</span></td>
        <td class="num">
          {{ if gt .SamplesRun 0 }}{{ .SamplesRun }}{{ else if gt .Pairs 0 }}{{ .Pairs }}{{ else }}—{{ end }}
          {{ if or (gt .DroppedCand 0) (gt .DroppedBase 0) }}
          <span style="font-size:11px;color:var(--muted);display:block">dropped: {{ .DroppedCand }} cand, {{ .DroppedBase }} base</span>
          {{ end }}
        </td>{{ if $.HasTokens }}
        <td class="num">{{ if .Tokens }}{{ .Tokens.Total }}{{ if gt .Tokens.Reasoning 0 }} (+R {{ .Tokens.Reasoning }}){{ end }}{{ else }}—{{ end }}</td>{{ end }}
      </tr>
      {{ else }}
      <tr>
        <td colspan="{{ if and .HasTokens .HasAffinity }}9{{ else if or .HasTokens .HasAffinity }}8{{ else }}7{{ end }}" class="empty-state">No task comparisons recorded.</td>
      </tr>
      {{ end }}
    </tbody>
  </table>
</div>
{{ end }}`

const comparisonDetailTemplate = `
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{ .Name }} · Comparison · eval-runner</title>
  <style>` + baseCSS + `</style>
</head>
<body>
  <header class="header-bar">
    <div class="header-brand">
      <a href="/runs" class="header-logo">eval-runner</a>
      <nav class="nav-links">
        <a href="/pulls" class="nav-link {{ if eq .ActiveTab "pulls" }}active{{ end }}">Pull Requests</a>
        <a href="/runs" class="nav-link {{ if eq .ActiveTab "runs" }}active{{ end }}">Runs</a>
        <a href="/comparisons" class="nav-link {{ if eq .ActiveTab "comparisons" }}active{{ end }}">Comparisons</a>
      </nav>
    </div>
    <div style="font-size:13px;color:var(--muted)">namespace: <span class="mono">{{ .Namespace }}</span></div>
  </header>

  <main class="container">
    <div class="card">
      <div class="card-header">
        <div style="display:flex;align-items:center;gap:12px">
          <h1 class="card-title mono">{{ .Name }}</h1>
        </div>
        <div style="font-size:13px;color:var(--muted)"><a href="/comparisons">← Back to Comparisons</a></div>
      </div>
      <div class="card-body">
        <div class="verdict">
          <span class="chip {{ .VerdictChip }}" style="font-size:16px;padding:4px 14px"><span class="dot"></span>{{ .Verdict }}</span>
          <span class="verdict-headline">{{ .Headline }}</span>{{ if .HasAffinity }}
          <span class="chip neutral" style="font-size:13px;padding:2px 8px">{{ .AffinityMatched }} matched</span>{{ end }}
        </div>

        {{ if .Message }}
        <div class="msg-alert"><b>Status:</b> {{ .Message }}</div>
        {{ end }}

        {{ template "comparison_table" . }}
        {{- if .Actions }}
        <ul style="margin:16px 0 0;padding-left:20px;font-size:14px;color:var(--ink-2)">
          {{ range .Actions }}
          <li>run {{ .SuggestedNext }} more samples{{ if and .ShowTaskID .TaskID }} ({{ .TaskID }}){{ end }}</li>
          {{ end }}
        </ul>
        {{- end }}
        {{- if or .Histories .HistoryNote }}
        <h2 style="font-size:17px;font-weight:600;margin:24px 0 12px">Confidence Interval History</h2>
        {{- if .HistoryNote }}
        <div style="font-size:13px;color:var(--muted);padding:8px 0">{{ .HistoryNote }}</div>
        {{- end }}
        {{- range .Histories }}
        <div style="margin-top:16px">
          <div class="mono" style="font-weight:600;font-size:14px;margin-bottom:8px">{{ .TaskID }}</div>
          <div style="overflow-x:auto">
            <svg viewBox="{{ .SVG.ViewBox }}" width="100%" height="220" style="max-width:640px;display:block">
              <!-- Grid / Axes -->
              <line x1="{{ .SVG.X0 }}" y1="{{ .SVG.Y1 }}" x2="{{ .SVG.X1 }}" y2="{{ .SVG.Y1 }}" stroke="var(--line)" stroke-width="1"/>
              <line x1="{{ .SVG.X0 }}" y1="{{ .SVG.Y0 }}" x2="{{ .SVG.X0 }}" y2="{{ .SVG.Y1 }}" stroke="var(--line)" stroke-width="1"/>

              <!-- Equivalence margin reference line -->
              <line x1="{{ .SVG.X0 }}" y1="{{ .SVG.MarginY }}" x2="{{ .SVG.X1 }}" y2="{{ .SVG.MarginY }}" stroke="var(--muted)" stroke-dasharray="4 4" stroke-width="1"/>
              <text x="{{ .SVG.X1 }}" y="{{ .SVG.MarginY }}" dx="-4" dy="-4" text-anchor="end" font-size="11" fill="var(--muted)" font-family="var(--sans)">{{ .SVG.MarginLabel }}</text>

              <!-- Polyline series -->
              <polyline points="{{ .SVG.PointsAttr }}" fill="none" stroke="var(--indigo)" stroke-width="2"/>

              <!-- Points -->
              {{ range .SVG.Circles }}
              <circle cx="{{ .CX }}" cy="{{ .CY }}" r="{{ .R }}" fill="var(--indigo)" stroke="#fff" stroke-width="2"/>
              {{ end }}

              <!-- Final point label -->
              <text x="{{ .SVG.FinalLabel.X }}" y="{{ .SVG.FinalLabel.Y }}" font-size="11" font-weight="600" fill="var(--indigo)" font-family="var(--mono)">{{ .SVG.FinalLabel.Text }}</text>

              <!-- X Ticks & Labels -->
              {{ range .SVG.XAxisTicks }}
              <line x1="{{ .X }}" y1="{{ .Y1 }}" x2="{{ .X }}" y2="{{ .Y2 }}" stroke="var(--line)" stroke-width="1"/>
              <text x="{{ .X }}" y="{{ .TextY }}" text-anchor="middle" font-size="11" fill="var(--muted)" font-family="var(--mono)">{{ .Text }}</text>
              {{ end }}

              <!-- Y Ticks & Labels -->
              {{ range .SVG.YAxisTicks }}
              <text x="{{ .X }}" y="{{ .Y }}" text-anchor="end" font-size="11" fill="var(--muted)" font-family="var(--mono)">{{ .Text }}</text>
              {{ end }}
            </svg>
          </div>

          <div style="overflow-x:auto;margin-top:12px;margin-bottom:20px">
            <table class="ci history-table">
              <thead>
                <tr>
                  <th>n</th>
                  <th>Δ</th>
                  <th>95% CI</th>
                  <th>decision</th>
                </tr>
              </thead>
              <tbody>
                {{ range .Points }}
                <tr>
                  <td class="num">{{ .N }}</td>
                  <td class="num">{{ .DeltaFormatted }}</td>
                  <td class="num">{{ .CIFormatted }}</td>
                  <td><span class="chip {{ .DecisionChip }}"><span class="dot"></span>{{ .Decision }}</span></td>
                </tr>
                {{ end }}
              </tbody>
            </table>
          </div>
        </div>
        {{- end }}
        {{- end }}
        {{- if .Batches }}
        <h2 style="font-size:17px;font-weight:600;margin:24px 0 12px">Batches</h2>
        <div style="overflow-x:auto">
          <table class="ci">
            <thead>
              <tr>
                <th>batch</th>
                <th>phase</th>
                <th>samples</th>
                <th>candidate run</th>
                <th>baseline run</th>
              </tr>
            </thead>
            <tbody>
              {{ range .Batches }}
              <tr>
                <td class="mono" style="font-weight:600">batch {{ .Index }}</td>
                <td><span class="chip {{ .PhaseChip }}"><span class="dot"></span>{{ .Phase }}</span></td>
                <td class="num">{{ if .SampleRange }}{{ .SampleRange }}{{ else }}—{{ end }}</td>
                <td><a href="/runs/{{ $.Namespace }}/{{ .CandidateRun }}" class="mono" style="font-size:13px">{{ .CandidateRun }}</a></td>
                <td><a href="/runs/{{ $.Namespace }}/{{ .BaselineRun }}" class="mono" style="font-size:13px">{{ .BaselineRun }}</a></td>
              </tr>
              {{ end }}
            </tbody>
          </table>
        </div>
        {{- end }}
      </div>

      <div class="card-footer">
        <span><b>Base:</b> commit <span class="mono">{{ if .BaseCommitShort }}{{ .BaseCommitShort }}{{ else }}—{{ end }}</span> (<a href="/runs/{{ .Namespace }}/{{ .BaseRun }}">{{ .BaseRun }}</a>)</span>
        <span><b>Candidate:</b> commit <span class="mono">{{ if .CandCommitShort }}{{ .CandCommitShort }}{{ else }}—{{ end }}</span> (<a href="/runs/{{ .Namespace }}/{{ .CandRun }}">{{ .CandRun }}</a>)</span>
        <span><b>Samples:</b> {{ if .SamplesSummary }}{{ .SamplesSummary }}{{ else }}{{ .TotalSamples }} run · {{ .TotalPairs }} paired{{ if or (gt .TotalDroppedCand 0) (gt .TotalDroppedBase 0) }} · dropped: {{ .TotalDroppedCand }} cand, {{ .TotalDroppedBase }} base{{ end }}{{ end }}</span>
        {{- if .Batches }}
        <span><b>Batches:</b> {{ len .Batches }}</span>
        {{- end }}
        {{ if .SummaryPath }}
        <span style="font-size:12px" class="mono" title="{{ .SummaryPath }}">{{ .SummaryPath }}</span>
        {{ end }}
      </div>
    </div>
  </main>
</body>
</html>
`

const trajectoryCSS = `
.breadcrumb { font-size: 13px; color: var(--muted); margin-bottom: 14px; }
.breadcrumb a { color: var(--indigo); text-decoration: none; }
.breadcrumb a:hover { text-decoration: underline; }
.sample-link { display: inline-flex; align-items: center; gap: 8px; color: var(--ink); text-decoration: none; font-weight: 500; }
.sample-link:hover { text-decoration: underline; color: var(--indigo); }
.st { width: 8px; height: 8px; border-radius: 50%; display: inline-block; flex-shrink: 0; }
.st.p { background: var(--good); }
.st.f { background: var(--bad); }
.preview-cell { max-width: 320px; }
.preview-text { font-family: var(--mono); font-size: 12px; color: var(--ink-2); white-space: pre-wrap; word-break: break-word; line-height: 1.4; margin: 0; max-height: 4.2em; overflow: hidden; }
.btn-page { font-size: 13px; padding: 4px 10px; border: 1px solid var(--line); border-radius: 4px; color: var(--ink); text-decoration: none; background: var(--surface); }
.btn-page:hover { background: var(--canvas); }
.ev { display: flex; gap: 18px; padding: 12px 16px; border-radius: 10px; margin-bottom: 8px; font-size: 14px; align-items: flex-start; background: var(--canvas); border: 1px solid var(--line); }
.ev .k { width: 100px; flex: none; font-family: var(--mono); font-size: 12px; color: var(--muted); padding-top: 2px; text-transform: uppercase; letter-spacing: 0.05em; }
.ev .b { flex: 1; font-family: var(--mono); font-size: 13px; white-space: pre-wrap; word-break: break-word; line-height: 1.5; margin: 0; color: var(--ink); }
.ev.tool { background: var(--indigo-soft); border-color: #C7D2FE; }
.ev.tool .k { color: var(--indigo); }
.ev.bad { background: var(--bad-soft); border-color: #FECACA; }
.ev.bad .k { color: var(--bad); }
`

const taskSamplesListTemplate = `
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{ .TaskID }} · Samples · eval-runner</title>
  <style>` + baseCSS + trajectoryCSS + `</style>
</head>
<body>
  <header class="header-bar">
    <div class="header-brand">
      <a href="/runs" class="header-logo">eval-runner</a>
      <nav class="nav-links">
        <a href="/pulls" class="nav-link {{ if eq .ActiveTab "pulls" }}active{{ end }}">Pull Requests</a>
        <a href="/runs" class="nav-link {{ if eq .ActiveTab "runs" }}active{{ end }}">Runs</a>
        <a href="/comparisons" class="nav-link {{ if eq .ActiveTab "comparisons" }}active{{ end }}">Comparisons</a>
      </nav>
    </div>
    <div style="font-size:13px;color:var(--muted)">namespace: <span class="mono">{{ .Namespace }}</span></div>
  </header>

  <main class="container">
    <div class="card">
      <div class="breadcrumb">
        <a href="/runs">Runs</a> / <a href="/runs/{{ .Namespace }}/{{ .RunName }}">{{ .RunName }}</a> / <span class="mono">{{ .TaskID }}</span>
      </div>

      <div class="card-header" style="align-items:flex-start;flex-direction:column;gap:8px">
        <h1 class="card-title mono" style="font-size:22px">{{ .TaskID }}</h1>
        <div class="meta mono" style="font-size:12px;color:var(--muted)">
          commit <span class="mono">{{ .Commit }}</span> · {{ .Model }} · <span class="log-path mono" title="{{ .LogPath }}">{{ .LogPath }}</span>
        </div>
      </div>

      {{ if .IsTruncatedLog }}
      <div class="msg-alert" style="margin-bottom:16px">
        <strong>Log Truncated:</strong> {{ .TruncatedMessage }}
      </div>
      {{ else }}
      <table class="table" style="margin-top:16px">
        <thead>
          <tr>
            <th style="width:140px">sample</th>
            <th style="width:90px">score</th>
            <th>input preview</th>
            <th>output preview</th>
            <th style="width:90px">latency</th>
            <th style="width:90px">tokens</th>
          </tr>
        </thead>
        <tbody>
          {{ range .Samples }}
          <tr>
            <td class="mono">
              <a href="{{ .Link }}" class="sample-link">
                <span class="st {{ if eq .Status "pass" }}p{{ else if eq .Status "fail" }}f{{ end }}"></span>
                {{ .ID }}
              </a>
            </td>
            <td>
              <span class="chip chip-{{ .StatusClass }}">{{ .Score }}</span>
            </td>
            <td class="preview-cell"><pre class="preview-text">{{ .InputPreview }}</pre></td>
            <td class="preview-cell"><pre class="preview-text">{{ .OutputPreview }}</pre></td>
            <td class="mono num">{{ .LatencyStr }}</td>
            <td class="mono num">{{ .TokensStr }}</td>
          </tr>
          {{ else }}
          <tr>
            <td colspan="6" class="empty-state">No samples recorded for this task.</td>
          </tr>
          {{ end }}
        </tbody>
      </table>

      {{ if or .HasPrev .HasNext }}
      <div class="pagination-bar" style="display:flex;justify-content:space-between;align-items:center;margin-top:16px;padding:8px 0;border-top:1px solid var(--line)">
        <div style="font-size:13px;color:var(--muted)">
          Showing {{ .ShowingStart }}–{{ .ShowingEnd }} of {{ .TotalSamples }} samples
        </div>
        <div style="display:flex;gap:8px">
          {{ if .HasPrev }}
          <a href="{{ .PrevLink }}" class="btn-page">← Previous</a>
          {{ end }}
          {{ if .HasNext }}
          <a href="{{ .NextLink }}" class="btn-page">Next →</a>
          {{ end }}
        </div>
      </div>
      {{ end }}
      {{ end }}
    </div>
  </main>
</body>
</html>
`

const sampleTranscriptTemplate = `
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{ .SampleID }} · {{ .TaskID }} · eval-runner</title>
  <style>` + baseCSS + trajectoryCSS + `</style>
</head>
<body>
  <header class="header-bar">
    <div class="header-brand">
      <a href="/runs" class="header-logo">eval-runner</a>
      <nav class="nav-links">
        <a href="/pulls" class="nav-link {{ if eq .ActiveTab "pulls" }}active{{ end }}">Pull Requests</a>
        <a href="/runs" class="nav-link {{ if eq .ActiveTab "runs" }}active{{ end }}">Runs</a>
        <a href="/comparisons" class="nav-link {{ if eq .ActiveTab "comparisons" }}active{{ end }}">Comparisons</a>
      </nav>
    </div>
    <div style="font-size:13px;color:var(--muted)">namespace: <span class="mono">{{ .Namespace }}</span></div>
  </header>

  <main class="container">
    <div class="card">
      <div class="breadcrumb">
        <a href="/runs">Runs</a> / <a href="/runs/{{ .Namespace }}/{{ .RunName }}">{{ .RunName }}</a> / <a href="/runs/{{ .Namespace }}/{{ .RunName }}/tasks/{{ .EscapedTaskID }}/samples">{{ .TaskID }}</a> / <span class="mono">{{ .SampleID }}</span>
      </div>

      <div class="card-header" style="align-items:flex-start;flex-direction:column;gap:8px">
        <h2 style="font-size:20px;font-weight:600;margin:0">
          <span class="mono">{{ .SampleID }}</span> · <span class="chip chip-{{ .StatusClass }}">{{ .StatusUpper }}</span> · {{ .LatencyStr }} · {{ .TokensStr }} tokens
        </h2>
        <div class="meta mono" style="font-size:12px;color:var(--muted)">
          <span class="log-path mono" title="{{ .LogPath }}">{{ .LogPath }}</span> · inspect log format
        </div>
      </div>

      {{ if .IsTruncatedLog }}
      <div class="msg-alert" style="margin-bottom:16px">
        <strong>Log Truncated:</strong> {{ .TruncatedMessage }}
      </div>
      {{ else }}

      <div class="transcript" style="margin-top:20px">
        {{ range .Rows }}
        <div class="ev {{ .Class }}">
          <span class="k">{{ .Kind }}</span>
          <pre class="b">{{ .Content }}</pre>
        </div>
        {{ else }}
        <div class="empty-state">No transcript events recorded for this sample.</div>
        {{ end }}

        {{ if .IsEventsTruncated }}
        <div class="truncation-marker mono" style="margin-top:12px;padding:10px 14px;background:var(--neutral-soft);color:var(--muted);border-radius:6px;font-size:13px;text-align:center">
          {{ .EventsTruncatedMessage }}
        </div>
        {{ end }}
      </div>

      <div class="foot" style="margin-top:24px;padding-top:12px;border-top:1px solid var(--line);font-size:12px;color:var(--muted);text-align:right">
        open format · any agent can read it
      </div>
      {{ end }}
    </div>
  </main>
</body>
</html>
`

var (
	tmplRunsList         = template.Must(template.New("runs_list").Parse(runsListTemplate))
	tmplRunDetail        = template.Must(template.New("run_detail").Parse(runDetailTemplate))
	tmplComparisonsList  = template.Must(template.New("comparisons_list").Parse(comparisonsListTemplate))
	tmplComparisonTable  = template.Must(template.New("comparison_table").Parse(ComparisonTableTemplate))
	tmplComparisonDetail = template.Must(template.Must(template.New("comparison_detail").Parse(ComparisonTableTemplate)).Parse(comparisonDetailTemplate))
	tmplTaskSamplesList  = template.Must(template.New("task_samples_list").Parse(taskSamplesListTemplate))
	tmplSampleTranscript = template.Must(template.New("sample_transcript").Parse(sampleTranscriptTemplate))
	tmplPullsList        = template.Must(template.New("pulls_list").Parse(pullsListTemplate))
	tmplPullDetail       = template.Must(template.New("pull_detail").Parse(pullDetailTemplate))
	tmplSeriesList       = template.Must(template.New("series_list").Parse(seriesListTemplate))
	tmplSeriesDetail     = template.Must(template.New("series_detail").Parse(seriesDetailTemplate))

	// TmplComparisonTable is the parsed template for the comparison table sub-template.
	TmplComparisonTable = tmplComparisonTable
)

const pullsListTemplate = `
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Pull Requests · eval-runner</title>
  <style>` + baseCSS + `</style>
</head>
<body>
  <header class="header-bar">
    <div class="header-brand">
      <a href="/runs" class="header-logo">eval-runner</a>
      <nav class="nav-links">
        <a href="/pulls" class="nav-link {{ if eq .ActiveTab "pulls" }}active{{ end }}">Pull Requests</a>
        <a href="/runs" class="nav-link {{ if eq .ActiveTab "runs" }}active{{ end }}">Runs</a>
        <a href="/comparisons" class="nav-link {{ if eq .ActiveTab "comparisons" }}active{{ end }}">Comparisons</a>
      </nav>
    </div>
    <div style="font-size:13px;color:var(--muted)">namespace: <span class="mono">{{ .Namespace }}</span></div>
  </header>

  <main class="container">
    <div class="card">
      <div class="card-header">
        <h1 class="card-title">Pull Requests</h1>
        <span style="font-size:13px;color:var(--muted)">{{ .TotalCount }} total</span>
      </div>
      <div style="overflow-x:auto">
        <table class="ci">
          <thead>
            <tr>
              <th>Repository</th>
              <th>PR</th>
              <th>Head</th>
              <th>Verdict</th>
              <th>Samples</th>
              <th>Batches</th>
              <th>Completed</th>
              <th>Comparison</th>
            </tr>
          </thead>
          <tbody>
            {{ range .Pulls }}
            <tr>
              <td><a href="{{ .HistoryLink }}" style="font-weight:600">{{ .Repository }}</a></td>
              <td><a href="{{ .GitHubURL }}" target="_blank" rel="noopener">#{{ .Number }}</a></td>
              <td><span class="mono" style="font-size:13px">{{ .HeadSHAShort }}</span></td>
              <td><span class="chip {{ .VerdictChip }}"><span class="dot"></span>{{ .Verdict }}</span>{{ if ne .PImprovement "—" }} <span class="mono" style="font-size:12px;color:var(--muted)">(P={{ .PImprovement }})</span>{{ end }}</td>
              <td class="num">{{ .SamplesRun }}</td>
              <td class="num">{{ .Batches }}</td>
              <td style="color:var(--muted);font-size:13px">{{ .CompletionTime }}</td>
              <td><a href="{{ .ComparisonLink }}" class="mono" style="font-size:13px">{{ .ComparisonName }}</a></td>
            </tr>
            {{ else }}
            <tr>
              <td colspan="8" class="empty-state">No pull requests found in namespace {{ .Namespace }}.</td>
            </tr>
            {{ end }}
          </tbody>
        </table>
      </div>
      {{ if or .HasPrev .HasMore }}
      <div class="pagination" style="padding:14px 24px;border-top:1px solid var(--line);display:flex;justify-content:space-between;align-items:center;font-size:13px">
        <div>
          {{ if .HasPrev }}<a href="{{ .PrevLink }}">← Previous</a>{{ end }}
        </div>
        <div style="color:var(--muted)">{{ .ShowingRange }}</div>
        <div>
          {{ if .HasMore }}<a href="{{ .NextLink }}">More →</a>{{ end }}
        </div>
      </div>
      {{ end }}
    </div>
  </main>
</body>
</html>
`

const pullDetailTemplate = `
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{ .Repository }} #{{ .Number }} · Pull Request · eval-runner</title>
  <style>` + baseCSS + `</style>
</head>
<body>
  <header class="header-bar">
    <div class="header-brand">
      <a href="/runs" class="header-logo">eval-runner</a>
      <nav class="nav-links">
        <a href="/pulls" class="nav-link {{ if eq .ActiveTab "pulls" }}active{{ end }}">Pull Requests</a>
        <a href="/runs" class="nav-link {{ if eq .ActiveTab "runs" }}active{{ end }}">Runs</a>
        <a href="/comparisons" class="nav-link {{ if eq .ActiveTab "comparisons" }}active{{ end }}">Comparisons</a>
      </nav>
    </div>
    <div style="font-size:13px;color:var(--muted)">namespace: <span class="mono">{{ .Namespace }}</span></div>
  </header>

  <main class="container">
    <div class="card">
      <div class="card-header">
        <div style="display:flex;align-items:center;gap:12px">
          <h1 class="card-title">{{ .Repository }} <a href="{{ .GitHubURL }}" target="_blank" rel="noopener">#{{ .Number }}</a></h1>
        </div>
        <div style="font-size:13px;color:var(--muted)"><a href="{{ .BackLink }}">← Back to Pull Requests</a></div>
      </div>
      <div class="card-body">
        <div style="overflow-x:auto">
          <table class="ci">
            <thead>
              <tr>
                <th>Comparison</th>
                <th>Head</th>
                <th>Base</th>
                <th>Verdict</th>
                <th>P(imp)</th>
                <th>Samples</th>
                <th>Batches</th>
                <th>Completed</th>
                <th>Age</th>
              </tr>
            </thead>
            <tbody>
              {{ range .Comparisons }}
              <tr>
                <td><a href="{{ .Link }}" class="mono" style="font-weight:600">{{ .Name }}</a></td>
                <td><span class="mono" style="font-size:13px">{{ .HeadSHAShort }}</span></td>
                <td><span class="mono" style="font-size:13px">{{ .BaseSHAShort }}</span></td>
                <td><span class="chip {{ .VerdictChip }}"><span class="dot"></span>{{ .Verdict }}</span></td>
                <td class="num">{{ .PImprovement }}</td>
                <td class="num">{{ .SamplesRun }}</td>
                <td class="num">{{ .Batches }}</td>
                <td style="color:var(--muted);font-size:13px">{{ .CompletionTime }}</td>
                <td style="color:var(--muted);font-size:13px">{{ .Age }}</td>
              </tr>
              {{ else }}
              <tr>
                <td colspan="9" class="empty-state">No comparisons recorded for this pull request.</td>
              </tr>
              {{ end }}
            </tbody>
          </table>
        </div>
      </div>
      <div class="card-footer">
        <span><b>Repository:</b> {{ .Repository }}</span>
        <span><b>PR:</b> <a href="{{ .GitHubURL }}" target="_blank" rel="noopener">#{{ .Number }}</a></span>
        <span><b>Comparisons:</b> {{ .TotalCount }}</span>
      </div>
    </div>
  </main>
</body>
</html>
`

// RenderPullsList renders the pull requests list template to the given writer.
func RenderPullsList(w io.Writer, view PullRequestsListView) error {
	return tmplPullsList.Execute(w, view)
}

// RenderPullDetail renders the pull request detail template to the given writer.
func RenderPullDetail(w io.Writer, view PullRequestDetailView) error {
	return tmplPullDetail.Execute(w, view)
}

// RenderTaskSamplesList renders the task samples list template to the given writer.
func RenderTaskSamplesList(w io.Writer, view TaskSamplesListView) error {
	return tmplTaskSamplesList.Execute(w, view)
}

// RenderSampleTranscript renders the sample transcript template to the given writer.
func RenderSampleTranscript(w io.Writer, view SampleTranscriptView) error {
	return tmplSampleTranscript.Execute(w, view)
}

// RenderComparisonTable renders the comparison table sub-template to the given writer.
func RenderComparisonTable(w io.Writer, view ComparisonTableView) error {
	if !view.HasTokens {
		for _, t := range view.Tasks {
			if t.Tokens != nil {
				view.HasTokens = true
				break
			}
		}
	}
	return tmplComparisonTable.ExecuteTemplate(w, "comparison_table", view)
}

const seriesListTemplate = `
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Series · eval-runner</title>
  <style>` + baseCSS + `</style>
</head>
<body>
  <header class="header-bar">
    <div class="header-brand">
      <a href="/runs" class="header-logo">eval-runner</a>
      <nav class="nav-links">
        <a href="/pulls" class="nav-link {{ if eq .ActiveTab "pulls" }}active{{ end }}">Pull Requests</a>
        <a href="/runs" class="nav-link {{ if eq .ActiveTab "runs" }}active{{ end }}">Runs</a>
        <a href="/comparisons" class="nav-link {{ if eq .ActiveTab "comparisons" }}active{{ end }}">Comparisons</a>
        <a href="/series" class="nav-link {{ if eq .ActiveTab "series" }}active{{ end }}">Series</a>
      </nav>
    </div>
    <div style="font-size:13px;color:var(--muted)">namespace: <span class="mono">{{ .Namespace }}</span></div>
  </header>

  <main class="container">
    <div class="card">
      <div class="card-header">
        <h1 class="card-title">Main-Branch Series</h1>
        <span style="font-size:13px;color:var(--muted)">{{ .TotalCount }} total</span>
      </div>
      <div style="overflow-x:auto">
        <table class="ci">
          <thead>
            <tr>
              <th>Repository</th>
              <th>Branch</th>
              <th>Runs</th>
              <th>Action</th>
            </tr>
          </thead>
          <tbody>
            {{ range .Series }}
            <tr>
              <td><a href="{{ .Link }}" style="font-weight:600">{{ .Repository }}</a></td>
              <td><span class="mono" style="font-size:13px">{{ .Branch }}</span></td>
              <td class="num">{{ .RunCount }}</td>
              <td><a href="{{ .Link }}" class="mono" style="font-size:13px">view series →</a></td>
            </tr>
            {{ else }}
            <tr>
              <td colspan="4" class="empty-state">No series found in namespace {{ .Namespace }}.</td>
            </tr>
            {{ end }}
          </tbody>
        </table>
      </div>
    </div>
  </main>
</body>
</html>
`

const seriesDetailTemplate = `
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{ .Repository }} ({{ .Branch }}) · Series · eval-runner</title>
  <style>` + baseCSS + `</style>
</head>
<body>
  <header class="header-bar">
    <div class="header-brand">
      <a href="/runs" class="header-logo">eval-runner</a>
      <nav class="nav-links">
        <a href="/pulls" class="nav-link {{ if eq .ActiveTab "pulls" }}active{{ end }}">Pull Requests</a>
        <a href="/runs" class="nav-link {{ if eq .ActiveTab "runs" }}active{{ end }}">Runs</a>
        <a href="/comparisons" class="nav-link {{ if eq .ActiveTab "comparisons" }}active{{ end }}">Comparisons</a>
        <a href="/series" class="nav-link {{ if eq .ActiveTab "series" }}active{{ end }}">Series</a>
      </nav>
    </div>
    <div style="font-size:13px;color:var(--muted)">namespace: <span class="mono">{{ .Namespace }}</span></div>
  </header>

  <main class="container">
    <div class="card">
      <div class="card-header">
        <div>
          <h1 class="card-title">
            <span>{{ .Repository }}</span>
            <span class="mono" style="font-size:16px;color:var(--muted);font-weight:normal">({{ .Branch }})</span>
          </h1>
          <div style="font-size:13px;color:var(--muted);margin-top:4px"><a href="{{ .BackLink }}">← Back to Series</a></div>
        </div>
        <span style="font-size:13px;color:var(--muted)">showing last {{ len .Items }} of {{ .TotalRuns }} commits</span>
      </div>
      <div class="card-body">
        {{- if .OrderedNote }}
        <div style="font-size:13px;color:var(--muted);margin-bottom:12px">{{ .OrderedNote }}</div>
        {{- end }}

        {{- if .Tasks }}
        <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap;margin-bottom:16px">
          <span style="font-size:13px;font-weight:600;color:var(--muted)">Task:</span>
          {{ range .Tasks }}
          <a href="{{ .Link }}" class="chip {{ if .Active }}good{{ else }}neutral{{ end }} mono" style="font-size:12px">{{ .ID }}</a>
          {{ end }}
        </div>
        {{- end }}

        {{- if .SelectedTask }}
        <div class="mono" style="font-weight:600;font-size:15px;margin-bottom:12px">{{ .SelectedTask }}</div>
        {{- end }}

        <div style="overflow-x:auto">
          <svg viewBox="{{ .SVG.ViewBox }}" width="100%" height="220" style="max-width:640px;display:block">
            <!-- Y-Axis Grid Lines & Labels -->
            {{ range .SVG.YAxisTicks }}
            <line x1="{{ $.SVG.X0 }}" y1="{{ .Y1 }}" x2="{{ $.SVG.X1 }}" y2="{{ .Y1 }}" stroke="var(--line)" stroke-width="1" stroke-dasharray="3,3"/>
            <text x="{{ .X }}" y="{{ .Y }}" text-anchor="end" font-size="11" fill="var(--muted)" font-family="var(--mono)">{{ .Text }}</text>
            {{ end }}

            <!-- Axes -->
            <line x1="{{ .SVG.X0 }}" y1="{{ .SVG.Y1 }}" x2="{{ .SVG.X1 }}" y2="{{ .SVG.Y1 }}" stroke="var(--line)" stroke-width="1"/>
            <line x1="{{ .SVG.X0 }}" y1="{{ .SVG.Y0 }}" x2="{{ .SVG.X0 }}" y2="{{ .SVG.Y1 }}" stroke="var(--line)" stroke-width="1"/>

            <!-- Shaded CI Polygons -->
            {{ range .SVG.Polygons }}
            <polygon points="{{ . }}" fill="var(--indigo)" fill-opacity="0.15" stroke="none"/>
            {{ end }}

            <!-- Polylines -->
            {{ range .SVG.Polylines }}
            <polyline points="{{ . }}" fill="none" stroke="var(--indigo)" stroke-width="2"/>
            {{ end }}

            <!-- Data Points -->
            {{ range .SVG.Circles }}
            <circle cx="{{ .CX }}" cy="{{ .CY }}" r="{{ .R }}" fill="var(--indigo)" stroke="#fff" stroke-width="2"/>
            {{ end }}

            <!-- X-Axis Ticks & Labels -->
            {{ range .SVG.XAxisTicks }}
            <line x1="{{ .X }}" y1="{{ .Y1 }}" x2="{{ .X }}" y2="{{ .Y2 }}" stroke="var(--line)" stroke-width="1"/>
            <text x="{{ .X }}" y="{{ .TextY }}" text-anchor="middle" font-size="11" fill="var(--muted)" font-family="var(--mono)">{{ .Text }}</text>
            {{ end }}
          </svg>
        </div>

        <div style="overflow-x:auto;margin-top:20px">
          <table class="ci">
            <thead>
              <tr>
                <th>Commit</th>
                <th>Time</th>
                <th>Phase</th>
                <th>Samples</th>
                <th>Rate</th>
                <th>95% CI</th>
                <th>Tokens</th>
                <th>Cost</th>
                <th>Run</th>
              </tr>
            </thead>
            <tbody>
              {{ range .Items }}
              <tr>
                <td><span class="mono" style="font-size:13px">{{ .CommitSHAShort }}</span></td>
                <td><span style="color:var(--muted);font-size:13px">{{ .CommitTimeStr }}</span></td>
                <td><span class="chip {{ .PhaseChip }}"><span class="dot"></span>{{ .Phase }}</span></td>
                <td class="num">{{ .Samples }}</td>
                <td class="num">{{ .RateStr }}</td>
                <td class="num" style="font-size:13px">{{ .CIStr }}</td>
                <td class="num">{{ .TokensStr }}</td>
                <td class="num">{{ .CostStr }}</td>
                <td><a href="{{ .RunLink }}" class="mono" style="font-size:13px">{{ .RunName }}</a></td>
              </tr>
              {{ else }}
              <tr>
                <td colspan="9" class="empty-state">No runs recorded for this series.</td>
              </tr>
              {{ end }}
            </tbody>
          </table>
        </div>
      </div>
      <div class="card-footer">
        <span><b>Repository:</b> {{ .Repository }}</span>
        <span><b>Branch:</b> {{ .Branch }}</span>
        <span><b>Total Runs:</b> {{ .TotalRuns }}</span>
      </div>
    </div>
  </main>
</body>
</html>
`

// RenderSeriesList renders the series list template to the given writer.
func RenderSeriesList(w io.Writer, view SeriesListView) error {
	return tmplSeriesList.Execute(w, view)
}

// RenderSeriesDetail renders the series detail template to the given writer.
func RenderSeriesDetail(w io.Writer, view SeriesDetailView) error {
	return tmplSeriesDetail.Execute(w, view)
}
