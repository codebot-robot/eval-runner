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
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
)

var (
	// inspectImportRe matches lines importing inspect_ai in code.
	// Aliases (e.g. import inspect_ai as ia) are noted as unsupported in ADR-031.
	inspectImportRe = regexp.MustCompile(`^\s*(?:import\s+inspect_ai\b|from\s+inspect_ai(?:\.|\s+).*import\b)`)

	// fnDefRe matches Python function definitions at column 0.
	fnDefRe = regexp.MustCompile(`^(?:def|async\s+def)\s+([a-zA-Z_][a-zA-Z0-9_]*)`)

	// ErrNoInspectTasksFound is returned when no Inspect tasks are discovered during scan.
	ErrNoInspectTasksFound = errors.New("no Inspect tasks found")
)

// QuoteState tracks whether Python parsing is currently inside a triple-quoted string.
type QuoteState struct {
	InTripleQuote bool
	Closer        string // `"""` or `'''`
}

// Update scans the entire line for `"""` and `”'` tokens in order, toggling on each,
// with the first opener fixing which closer applies.
func (s *QuoteState) Update(line string) {
	i := 0
	for i < len(line) {
		if !s.InTripleQuote {
			idxDouble := strings.Index(line[i:], `"""`)
			idxSingle := strings.Index(line[i:], `'''`)
			if idxDouble == -1 && idxSingle == -1 {
				break
			}
			if idxDouble != -1 && (idxSingle == -1 || idxDouble < idxSingle) {
				s.InTripleQuote = true
				s.Closer = `"""`
				i += idxDouble + 3
			} else {
				s.InTripleQuote = true
				s.Closer = `'''`
				i += idxSingle + 3
			}
		} else {
			idx := strings.Index(line[i:], s.Closer)
			if idx == -1 {
				break
			}
			s.InTripleQuote = false
			s.Closer = ""
			i += idx + 3
		}
	}
}

// stripPythonComment removes trailing comments starting with '#' outside single/double quotes.
func stripPythonComment(s string) string {
	inSingle := false
	inDouble := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == '\'' && !inDouble {
			inSingle = !inSingle
		} else if c == '"' && !inSingle {
			inDouble = !inDouble
		} else if c == '#' && !inSingle && !inDouble {
			return s[:i]
		}
	}
	return s
}

// rawTask represents a discovered Inspect task before disambiguation and capping.
type rawTask struct {
	relPath    string
	dir        string
	filename   string
	stem       string
	function   string
	initialID  string
	disambigID string
}

// ScanInspectFile parses a single Python source file for Inspect tasks.
// Returns tasks found and whether the file imported inspect_ai.
func ScanInspectFile(relPath string, content []byte) ([]rawTask, bool, error) {
	lines := strings.Split(string(content), "\n")
	var state QuoteState

	relClean := filepath.ToSlash(filepath.Clean(relPath))
	if strings.HasPrefix(relClean, "./") {
		relClean = strings.TrimPrefix(relClean, "./")
	}
	dir := path.Dir(relClean)
	if dir == "." {
		dir = ""
	}
	filename := path.Base(relClean)
	stem := strings.TrimSuffix(filename, path.Ext(filename))

	importsInspectAI := false
	armed := false
	parenDepth := 0
	var foundFunctions []string

	for _, line := range lines {
		// Rule: decide whether a line is code from the state at the start of the line
		isCodeLine := !state.InTripleQuote

		if isCodeLine {
			trimmed := strings.TrimSpace(line)

			// Rule: Check for inspect_ai import
			if !strings.HasPrefix(trimmed, "#") && inspectImportRe.MatchString(line) {
				importsInspectAI = true
			}

			// If armed across multi-line decorator arguments, balance parentheses
			if armed && parenDepth > 0 {
				codeOnly := stripPythonComment(line)
				parenDepth += strings.Count(codeOnly, "(") - strings.Count(codeOnly, ")")
				if parenDepth < 0 {
					parenDepth = 0
				}
				state.Update(line)
				continue
			}

			// Decorators only at column 0 (no leading whitespace)
			if len(line) > 0 && line[0] == '@' {
				var isTargetDecorator bool
				var rem string
				if strings.HasPrefix(line, "@task") {
					rem = line[len("@task"):]
					if rem == "" || rem[0] == '(' || rem[0] == ' ' || rem[0] == '\t' || rem[0] == '#' {
						isTargetDecorator = true
					}
				} else if strings.HasPrefix(line, "@inspect_ai.task") {
					rem = line[len("@inspect_ai.task"):]
					if rem == "" || rem[0] == '(' || rem[0] == ' ' || rem[0] == '\t' || rem[0] == '#' {
						isTargetDecorator = true
					}
				}

				if isTargetDecorator {
					codeOnly := stripPythonComment(line)
					net := strings.Count(codeOnly, "(") - strings.Count(codeOnly, ")")
					armed = true
					if net > 0 {
						parenDepth = net
					} else {
						parenDepth = 0
					}
					state.Update(line)
					continue
				}

				// If another stacked decorator is encountered while armed
				if armed {
					codeOnly := stripPythonComment(line)
					net := strings.Count(codeOnly, "(") - strings.Count(codeOnly, ")")
					if net > 0 {
						parenDepth = net
					}
					state.Update(line)
					continue
				}
			}

			// When decorator is armed and arguments are balanced, look for function def
			if armed && parenDepth == 0 {
				if trimmed == "" {
					state.Update(line)
					continue
				}
				if strings.HasPrefix(trimmed, "#") {
					state.Update(line)
					continue
				}
				// Function def must be at column 0
				if len(line) > 0 && (line[0] != ' ' && line[0] != '\t') {
					if matches := fnDefRe.FindStringSubmatch(line); len(matches) > 1 {
						fnName := matches[1]
						foundFunctions = append(foundFunctions, fnName)
						armed = false
						state.Update(line)
						continue
					}
				}
				// Any other non-empty non-comment line at column 0 or indented outside def disarms
				armed = false
			}
		}

		// Update quote state after evaluating the line
		state.Update(line)
	}

	if !importsInspectAI {
		return nil, false, nil
	}

	tasks := make([]rawTask, 0, len(foundFunctions))
	for _, fn := range foundFunctions {
		var initID, disID string
		if dir == "" {
			initID = fn
			disID = stem + "/" + fn
		} else {
			initID = dir + "/" + fn
			disID = dir + "/" + stem + "/" + fn
		}
		tasks = append(tasks, rawTask{
			relPath:    relClean,
			dir:        dir,
			filename:   filename,
			stem:       stem,
			function:   fn,
			initialID:  initID,
			disambigID: disID,
		})
	}

	return tasks, true, nil
}

// ScanResult holds the synthesized manifest, raw JSON, and discovery notes.
type ScanResult struct {
	Manifest      *v1alpha1.Manifest
	ManifestBytes []byte
	FileCapHit    bool
	TaskCapHit    bool
	Notes         []string
}

// ScanDir traverses a directory, scans Python files for Inspect tasks, and synthesizes a manifest.
func ScanDir(root string) (*ScanResult, error) {
	var fileList []string
	fileCapHit := false

	err := filepath.Walk(root, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == ".venv" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(info.Name(), ".py") {
			if len(fileList) >= MaxScanFiles {
				fileCapHit = true
				return filepath.SkipAll
			}
			fileList = append(fileList, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk directory %s: %w", root, err)
	}

	var allRaw []rawTask
	for _, f := range fileList {
		rel, err := filepath.Rel(root, f)
		if err != nil {
			rel = f
		}
		content, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", f, err)
		}
		tasks, ok, scanErr := ScanInspectFile(rel, content)
		if scanErr != nil {
			return nil, scanErr
		}
		if ok && len(tasks) > 0 {
			allRaw = append(allRaw, tasks...)
		}
	}

	if len(allRaw) == 0 {
		return nil, ErrNoInspectTasksFound
	}

	return synthesizeManifest(allRaw, fileCapHit)
}

// synthesizeManifest handles collision disambiguation, task capping, sorting, and manifest construction.
func synthesizeManifest(rawTasks []rawTask, fileCapHit bool) (*ScanResult, error) {
	// Group by initial ID to detect collisions
	idCounts := make(map[string]int)
	for _, t := range rawTasks {
		idCounts[t.initialID]++
	}

	var notes []string
	collisionGroups := make(map[string][]string)

	finalTasks := make([]v1alpha1.ManifestTask, 0, len(rawTasks))
	for _, t := range rawTasks {
		taskID := t.initialID
		if idCounts[t.initialID] > 1 {
			taskID = t.disambigID
			collisionGroups[t.initialID] = append(collisionGroups[t.initialID], t.disambigID)
		}

		paths := []string{"**"}
		if t.dir != "" {
			paths = []string{t.dir + "/**"}
		}

		finalTasks = append(finalTasks, v1alpha1.ManifestTask{
			ID:      taskID,
			Workdir: t.dir,
			Spec:    t.filename + "@" + t.function,
			Paths:   paths,
			Cohort:  v1alpha1.DefaultCohort,
		})
	}

	if len(collisionGroups) > 0 {
		var collKeys []string
		for k := range collisionGroups {
			collKeys = append(collKeys, k)
		}
		sort.Strings(collKeys)
		for _, k := range collKeys {
			disambigs := collisionGroups[k]
			sort.Strings(disambigs)
			notes = append(notes, fmt.Sprintf("task ID collision on %q: disambiguated to %s", k, strings.Join(disambigs, ", ")))
		}
	}

	taskCapHit := false
	if len(finalTasks) > MaxScanTasks {
		taskCapHit = true
		finalTasks = finalTasks[:MaxScanTasks]
		notes = append(notes, fmt.Sprintf("task cap reached (%d tasks)", MaxScanTasks))
	}

	if fileCapHit {
		notes = append(notes, fmt.Sprintf("file cap reached (%d files)", MaxScanFiles))
	}

	// Output sorted by id
	sort.Slice(finalTasks, func(i, j int) bool {
		return finalTasks[i].ID < finalTasks[j].ID
	})

	m := &v1alpha1.Manifest{
		Source: string(v1alpha1.ManifestSourceInspectScan),
		Tasks:  finalTasks,
	}

	if err := ValidateManifest(m); err != nil {
		return nil, fmt.Errorf("synthesized manifest validation failed: %w", err)
	}

	rawJSON, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal synthesized manifest: %w", err)
	}

	return &ScanResult{
		Manifest:      m,
		ManifestBytes: rawJSON,
		FileCapHit:    fileCapHit,
		TaskCapHit:    taskCapHit,
		Notes:         notes,
	}, nil
}

// ExtractAndScanTar extracts scan.tar into a safe temporary directory and scans for Inspect tasks.
func ExtractAndScanTar(tarBytes []byte) (*ScanResult, error) {
	if len(tarBytes) == 0 {
		return nil, ErrNoInspectTasksFound
	}

	tmpDir, err := os.MkdirTemp("", "eval-runner-scan-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir for scan: %w", err)
	}
	defer func() {
		if rmErr := os.RemoveAll(tmpDir); rmErr != nil {
			_ = rmErr
		}
	}()

	cleanTmpDir := filepath.Clean(tmpDir)
	tr := tar.NewReader(bytes.NewReader(tarBytes))

	fileCount := 0
	fileCapHit := false

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed reading scan tar: %w", err)
		}
		if header == nil {
			continue
		}

		cleanName := filepath.Clean(header.Name)
		if strings.HasPrefix(cleanName, "./") {
			cleanName = strings.TrimPrefix(cleanName, "./")
		}
		if cleanName == "." || cleanName == "" {
			continue
		}

		target := filepath.Join(cleanTmpDir, cleanName)
		if !strings.HasPrefix(target, cleanTmpDir+string(filepath.Separator)) {
			return nil, fmt.Errorf("path traversal detected in scan tar: %s", header.Name)
		}

		// Symlinks skipped
		if header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
			continue
		}

		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0700); err != nil {
				return nil, fmt.Errorf("failed to create directory %s: %w", target, err)
			}
			continue
		}

		if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA || header.Typeflag == 0 {
			fileCount++
			if fileCount > MaxScanFiles {
				fileCapHit = true
				break
			}
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return nil, fmt.Errorf("failed to create parent dir for %s: %w", target, err)
			}
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
			if err != nil {
				return nil, fmt.Errorf("failed to create file %s: %w", target, err)
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				if closeErr := outFile.Close(); closeErr != nil {
					return nil, fmt.Errorf("failed to write and close file %s: write err: %w, close err: %v", target, err, closeErr)
				}
				return nil, fmt.Errorf("failed to write file %s: %w", target, err)
			}
			if err := outFile.Close(); err != nil {
				return nil, fmt.Errorf("failed to close file %s: %w", target, err)
			}
		}
	}

	res, err := ScanDir(cleanTmpDir)
	if err != nil {
		return nil, err
	}
	if fileCapHit && !res.FileCapHit {
		res.FileCapHit = true
		res.Notes = append(res.Notes, fmt.Sprintf("file cap reached (%d files)", MaxScanFiles))
	}
	return res, nil
}
