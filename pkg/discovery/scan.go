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

package discovery

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/gke-labs/eval-runner/api/v1alpha1"
)

const (
	// MaxScanFiles is the maximum number of files visited during repository scan fallback.
	MaxScanFiles = 5000

	// MaxScanTasks is the maximum number of tasks synthesized during repository scan fallback.
	MaxScanTasks = 200

	// MaxScanFileReadBytes limits how much of a single Python file is read for AST/regex scanning.
	MaxScanFileReadBytes = 2 * 1024 * 1024 // 2 MiB
)

var (
	// inspectImportRegex matches imports of inspect_ai.
	inspectImportRegex = regexp.MustCompile(`(?m)^\s*(?:import\s+inspect_ai\b|from\s+inspect_ai(?:\.|\b))`)

	// taskDecoratorRegex matches @task or @inspect_ai.task decorator.
	taskDecoratorRegex = regexp.MustCompile(`^@(?:inspect_ai\.)?task(?:\s*\(.*)?$`)

	// funcDefRegex matches standard or async Python function definitions.
	funcDefRegex = regexp.MustCompile(`^(?:async\s+)?def\s+([a-zA-Z_][a-zA-Z0-9_]*)\s*\(`)
)

// ScanResult holds the outcome of an Inspect task fallback scan.
type ScanResult struct {
	Manifest                *v1alpha1.Manifest
	FilesCount              int
	TasksCount              int
	FileCapHit              bool
	TaskCapHit              bool
	DisambiguatedCollisions int
}

// CapMessage returns a human-readable note if a scan cap was reached or task IDs were disambiguated.
func (r *ScanResult) CapMessage() string {
	var parts []string
	if r.TaskCapHit && r.FileCapHit {
		parts = append(parts, fmt.Sprintf("scan capped at %d tasks and %d files", MaxScanTasks, MaxScanFiles))
	} else if r.TaskCapHit {
		parts = append(parts, fmt.Sprintf("task scan capped at %d tasks", MaxScanTasks))
	} else if r.FileCapHit {
		parts = append(parts, fmt.Sprintf("file scan capped at %d files", MaxScanFiles))
	}

	if r.DisambiguatedCollisions > 0 {
		parts = append(parts, fmt.Sprintf("disambiguated %d colliding task IDs with file stem", r.DisambiguatedCollisions))
	}

	return strings.Join(parts, "; ")
}

// gitignoreMatcher checks if paths match gitignore patterns.
type gitignoreMatcher struct {
	patterns []gitignorePattern
}

type gitignorePattern struct {
	raw       string
	glob      string
	dirOnly   bool
	matchName bool
}

func parseGitignore(r io.Reader) *gitignoreMatcher {
	var patterns []gitignorePattern
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		dirOnly := strings.HasSuffix(line, "/")
		line = strings.TrimSuffix(line, "/")
		line = strings.TrimPrefix(line, "/")

		// If pattern has no slash, it matches anywhere in the tree.
		matchName := !strings.Contains(line, "/")

		patterns = append(patterns, gitignorePattern{
			raw:       line,
			glob:      line,
			dirOnly:   dirOnly,
			matchName: matchName,
		})
	}
	return &gitignoreMatcher{patterns: patterns}
}

func (m *gitignoreMatcher) Matches(relPath string, isDir bool) bool {
	if m == nil || len(m.patterns) == 0 {
		return false
	}
	normPath := filepath.ToSlash(relPath)
	baseName := filepath.Base(normPath)

	for _, p := range m.patterns {
		if p.dirOnly && !isDir {
			continue
		}
		if p.matchName {
			if matched, err := doublestar.Match(p.glob, baseName); err == nil && matched {
				return true
			}
		}
		if matched, err := doublestar.Match(p.glob, normPath); err == nil && matched {
			return true
		}
		if matched, err := doublestar.Match("**/"+p.glob, normPath); err == nil && matched {
			return true
		}
		if isDir {
			if matched, err := doublestar.Match(p.glob+"/**", normPath); err == nil && matched {
				return true
			}
		}
	}
	return false
}

// isExcludedDir returns true if the directory name matches default excluded directory names.
func isExcludedDir(name string) bool {
	switch name {
	case ".git", ".venv", "node_modules", "vendor":
		return true
	default:
		return false
	}
}

// ScanInspectTasks walks root and synthesizes a Manifest from Inspect @task definitions.
func ScanInspectTasks(root string) (*ScanResult, error) {
	result := &ScanResult{
		Manifest: &v1alpha1.Manifest{
			Source: "inspect-scan",
			Tasks:  []v1alpha1.ManifestTask{},
		},
	}

	// Load root .gitignore if present
	var gi *gitignoreMatcher
	giBytes, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err == nil {
		gi = parseGitignore(bytes.NewReader(giBytes))
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read .gitignore: %w", err)
	}

	var allTasks []v1alpha1.ManifestTask

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil || relPath == "." {
			return nil
		}
		relPathSlash := filepath.ToSlash(relPath)

		if d.IsDir() {
			if isExcludedDir(d.Name()) {
				return filepath.SkipDir
			}
			if gi != nil && gi.Matches(relPathSlash, true) {
				return filepath.SkipDir
			}
			return nil
		}

		// Count visited files against file cap
		result.FilesCount++
		if result.FilesCount >= MaxScanFiles {
			result.FileCapHit = true
		}

		// Filter for Python files
		if !strings.HasSuffix(d.Name(), ".py") || strings.HasPrefix(d.Name(), ".") {
			if result.FileCapHit {
				return fs.SkipAll
			}
			return nil
		}

		// Check gitignore on file
		if gi != nil && gi.Matches(relPathSlash, false) {
			if result.FileCapHit {
				return fs.SkipAll
			}
			return nil
		}

		// Inspect file content
		tasks, parseErr := parsePythonInspectTasks(path, relPathSlash)
		if parseErr == nil && len(tasks) > 0 {
			for _, t := range tasks {
				if len(allTasks) >= MaxScanTasks {
					result.TaskCapHit = true
					break
				}
				allTasks = append(allTasks, t)
			}
		}

		if len(allTasks) >= MaxScanTasks {
			result.TaskCapHit = true
			return fs.SkipAll
		}
		if result.FileCapHit {
			return fs.SkipAll
		}
		return nil
	})

	if walkErr != nil && walkErr != fs.SkipAll {
		return nil, fmt.Errorf("error scanning directory %s: %w", root, walkErr)
	}

	// Disambiguate colliding task IDs
	idCounts := make(map[string]int, len(allTasks))
	for _, t := range allTasks {
		idCounts[t.ID]++
	}

	for i := range allTasks {
		t := &allTasks[i]
		if idCounts[t.ID] > 1 {
			specParts := strings.SplitN(t.Spec, "@", 2)
			fileStem := strings.TrimSuffix(specParts[0], ".py")
			funcName := specParts[1]

			if t.Workdir == "." || t.Workdir == "" {
				t.ID = fileStem + "/" + funcName
			} else {
				t.ID = t.Workdir + "/" + fileStem + "/" + funcName
			}
			result.DisambiguatedCollisions++
		}
	}

	// Sort tasks deterministically by task ID
	sort.Slice(allTasks, func(i, j int) bool {
		return allTasks[i].ID < allTasks[j].ID
	})

	result.Manifest.Tasks = allTasks
	result.TasksCount = len(allTasks)
	return result, nil
}

// parsePythonInspectTasks parses a Python file to extract Inspect @task decorated functions.
func parsePythonInspectTasks(filePath, relPathSlash string) ([]v1alpha1.ManifestTask, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			_ = closeErr
		}
	}()

	lr := io.LimitReader(f, MaxScanFileReadBytes)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}

	// Must import inspect_ai
	if !inspectImportRegex.Match(data) {
		return nil, nil
	}

	relDir := filepath.ToSlash(filepath.Dir(relPathSlash))
	baseName := filepath.Base(relPathSlash)

	var tasks []v1alpha1.ManifestTask
	scanner := bufio.NewScanner(bytes.NewReader(data))
	hasTaskDecorator := false
	inDecoratorParens := 0
	inTripleQuote := ""

	for scanner.Scan() {
		rawLine := scanner.Text()
		trimmedLine := strings.TrimSpace(rawLine)
		if trimmedLine == "" {
			continue
		}

		// Handle multiline docstrings / multiline strings
		if inTripleQuote != "" {
			if strings.Contains(trimmedLine, inTripleQuote) {
				inTripleQuote = ""
			}
			continue
		}

		// Check if a triple-quote string opens on this line
		if strings.HasPrefix(trimmedLine, `"""`) {
			if strings.Count(trimmedLine, `"""`)%2 != 0 {
				inTripleQuote = `"""`
				continue
			}
		} else if strings.HasPrefix(trimmedLine, `'''`) {
			if strings.Count(trimmedLine, `'''`)%2 != 0 {
				inTripleQuote = `'''`
				continue
			}
		}

		// Comments do not break decorator attachment, but are otherwise skipped
		if strings.HasPrefix(trimmedLine, "#") {
			continue
		}

		// Track arguments across multiline decorators, e.g.:
		// @task(
		//     name="x",
		// )
		if inDecoratorParens > 0 {
			inDecoratorParens += strings.Count(trimmedLine, "(") - strings.Count(trimmedLine, ")")
			if inDecoratorParens < 0 {
				inDecoratorParens = 0
			}
			continue
		}

		// Top-level decorators and function definitions must start at column 0.
		// Indented code (such as class methods) is not a standalone Inspect task.
		isColumnZero := !strings.HasPrefix(rawLine, " ") && !strings.HasPrefix(rawLine, "\t")
		if !isColumnZero {
			hasTaskDecorator = false
			continue
		}

		// Column 0 decorator
		if strings.HasPrefix(trimmedLine, "@") {
			if taskDecoratorRegex.MatchString(trimmedLine) {
				hasTaskDecorator = true
				openParens := strings.Count(trimmedLine, "(") - strings.Count(trimmedLine, ")")
				if openParens > 0 {
					inDecoratorParens = openParens
				}
			}
			// If it is another decorator (e.g. @solver), keep hasTaskDecorator set
			continue
		}

		// Column 0 function definition
		if matches := funcDefRegex.FindStringSubmatch(trimmedLine); len(matches) == 2 {
			if hasTaskDecorator {
				funcName := matches[1]

				var taskID, workdir string
				var paths []string
				if relDir == "." || relDir == "" {
					taskID = funcName
					workdir = "."
					paths = []string{"**"}
				} else {
					taskID = relDir + "/" + funcName
					workdir = relDir
					paths = []string{relDir + "/**"}
				}

				tasks = append(tasks, v1alpha1.ManifestTask{
					ID:        taskID,
					Workdir:   workdir,
					Spec:      baseName + "@" + funcName,
					Paths:     paths,
					Cohort:    v1alpha1.DefaultCohort,
					Resources: []v1alpha1.ManifestResource{},
				})
			}
			hasTaskDecorator = false
			continue
		}

		// Any other column-0 statement resets pending decorator
		hasTaskDecorator = false
	}

	return tasks, scanner.Err()
}
