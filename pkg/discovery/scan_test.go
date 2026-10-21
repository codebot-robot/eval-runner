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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
)

func TestScanInspectFile_RealFileDocsComprehension(t *testing.T) {
	// The scanner must scan this repository's own evals/docs_comprehension.py
	// and find both readme_comprehension and readme_comprehension_blind.
	path := filepath.Join("..", "..", "evals", "docs_comprehension.py")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}

	tasks, ok, err := ScanInspectFile("evals/docs_comprehension.py", content)
	if err != nil {
		t.Fatalf("ScanInspectFile failed: %v", err)
	}
	if !ok {
		t.Fatalf("expected evals/docs_comprehension.py to be identified as importing inspect_ai")
	}

	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks in docs_comprehension.py, got %d", len(tasks))
	}

	expected := map[string]bool{
		"readme_comprehension":       false,
		"readme_comprehension_blind": false,
	}
	for _, task := range tasks {
		if _, exists := expected[task.function]; !exists {
			t.Errorf("unexpected task function: %s", task.function)
		} else {
			expected[task.function] = true
		}
		if task.initialID != "evals/"+task.function {
			t.Errorf("expected initialID %q, got %q", "evals/"+task.function, task.initialID)
		}
	}
	for fn, found := range expected {
		if !found {
			t.Errorf("task function %q was not discovered", fn)
		}
	}
}

func TestScanDir_FixtureTree(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Task file 1: normal task
	task1Content := `import inspect_ai
from inspect_ai import task

@task
def task_one():
    pass
`
	// 2. Task file 2: inspect_ai.task form with trailing comment
	task2Content := `import inspect_ai

@inspect_ai.task # trailing comment
def task_two():
    pass
`
	// 3. Decoy without @task
	decoyContent := `import inspect_ai

def helper():
    return 42
`
	// 4. One under .venv (must be ignored)
	venvTaskContent := `from inspect_ai import task

@task
def venv_task():
    pass
`
	// 5. Docstring @task (must be ignored)
	docstringContent := `from inspect_ai import task

"""
@task
def docstring_fake():
    pass
'''
@task
def docstring_fake_single():
    pass
'''
"""

def real_helper():
    pass
`
	// 6. Multi-line @task(
	multilineContent := `from inspect_ai import task

@task(
    name="multi",
    # comment inside
)
def multiline_task():
    pass
`
	// 7. Indented (class) method (must be ignored)
	indentedContent := `from inspect_ai import task

class MySuite:
    @task
    def indented_method(self):
        pass
`
	// 8. Module-level X = """ constant before a @task
	moduleConstContent := `from inspect_ai import task

X = """
some constant
spanning lines
"""

@task
def task_after_const():
    pass
`
	// 9. A return f""" helper before a @task
	returnTemplateContent := `from inspect_ai import task

def my_helper():
    return f"""
    multiline
    template
    """

@task
def task_after_helper():
    pass
`
	// 10. Collision pair: two files in collision_dir with identical function 'common'
	collision1Content := `from inspect_ai import task

@task
def common():
    pass
`
	collision2Content := `from inspect_ai import task

@task
def common():
    pass
`

	// Write files to tmpDir
	writeFile := func(relPath, content string) {
		fullPath := filepath.Join(tmpDir, filepath.FromSlash(relPath))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0700); err != nil {
			t.Fatalf("failed to create dir for %s: %v", relPath, err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0600); err != nil {
			t.Fatalf("failed to write %s: %v", relPath, err)
		}
	}

	writeFile("evals/task1.py", task1Content)
	writeFile("evals/task2.py", task2Content)
	writeFile("evals/decoy.py", decoyContent)
	writeFile(".venv/ignored_task.py", venvTaskContent)
	writeFile("evals/docstring.py", docstringContent)
	writeFile("evals/multiline.py", multilineContent)
	writeFile("evals/indented.py", indentedContent)
	writeFile("evals/const.py", moduleConstContent)
	writeFile("evals/helper.py", returnTemplateContent)
	writeFile("collision_dir/file_a.py", collision1Content)
	writeFile("collision_dir/file_b.py", collision2Content)

	res, err := ScanDir(tmpDir)
	if err != nil {
		t.Fatalf("ScanDir failed: %v", err)
	}

	if res.Manifest.Source != string(v1alpha1.ManifestSourceInspectScan) {
		t.Errorf("expected manifest source %q, got %q", v1alpha1.ManifestSourceInspectScan, res.Manifest.Source)
	}

	// Verify tasks discovered
	tasksByID := make(map[string]v1alpha1.ManifestTask)
	for _, task := range res.Manifest.Tasks {
		tasksByID[task.ID] = task
	}

	// Expected discovered tasks:
	// - evals/task_one (from task1.py)
	// - evals/task_two (from task2.py)
	// - evals/multiline_task (from multiline.py)
	// - evals/task_after_const (from const.py)
	// - evals/task_after_helper (from helper.py)
	// - collision_dir/file_a/common (disambiguated from collision_dir/common)
	// - collision_dir/file_b/common (disambiguated from collision_dir/common)
	expectedIDs := []string{
		"collision_dir/file_a/common",
		"collision_dir/file_b/common",
		"evals/multiline_task",
		"evals/task_after_const",
		"evals/task_after_helper",
		"evals/task_one",
		"evals/task_two",
	}

	if len(res.Manifest.Tasks) != len(expectedIDs) {
		t.Fatalf("expected %d tasks, got %d: %+v", len(expectedIDs), len(res.Manifest.Tasks), res.Manifest.Tasks)
	}

	for _, id := range expectedIDs {
		task, ok := tasksByID[id]
		if !ok {
			t.Errorf("expected task id %q was not found in manifest", id)
			continue
		}
		if task.Cohort != v1alpha1.DefaultCohort {
			t.Errorf("expected cohort %q for %s, got %q", v1alpha1.DefaultCohort, id, task.Cohort)
		}
		if len(task.Resources) != 0 {
			t.Errorf("expected no resources for %s, got %+v", id, task.Resources)
		}
		if task.Workdir == "" {
			t.Errorf("expected non-empty workdir for %s", id)
		}
		if !strings.Contains(task.Spec, "@") {
			t.Errorf("expected spec to contain '@' for %s, got %q", id, task.Spec)
		}
	}

	// Verify collision note in Notes
	collisionNoteFound := false
	for _, n := range res.Notes {
		if strings.Contains(n, "collision_dir/common") && strings.Contains(n, "collision_dir/file_a/common") {
			collisionNoteFound = true
			break
		}
	}
	if !collisionNoteFound {
		t.Errorf("expected collision note for collision_dir/common in notes: %+v", res.Notes)
	}

	// Verify ignored items were not discovered
	ignoredIDs := []string{
		"evals/docstring_fake",
		"evals/docstring_fake_single",
		"evals/indented_method",
		"venv_task",
		".venv/venv_task",
	}
	for _, id := range ignoredIDs {
		if _, exists := tasksByID[id]; exists {
			t.Errorf("task %q should have been ignored but was discovered", id)
		}
	}
}

func TestScanDir_RootFiles(t *testing.T) {
	tmpDir := t.TempDir()

	rootTaskContent := `from inspect_ai import task

@task
def root_task():
    pass
`
	if err := os.WriteFile(filepath.Join(tmpDir, "root_eval.py"), []byte(rootTaskContent), 0600); err != nil {
		t.Fatalf("failed to write root_eval.py: %v", err)
	}

	res, err := ScanDir(tmpDir)
	if err != nil {
		t.Fatalf("ScanDir failed: %v", err)
	}

	if len(res.Manifest.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(res.Manifest.Tasks))
	}
	task := res.Manifest.Tasks[0]
	if task.ID != "root_task" {
		t.Errorf("expected id %q, got %q", "root_task", task.ID)
	}
	if task.Workdir != "" {
		t.Errorf("expected empty workdir for root task, got %q", task.Workdir)
	}
	if task.Spec != "root_eval.py@root_task" {
		t.Errorf("expected spec %q, got %q", "root_eval.py@root_task", task.Spec)
	}
	if len(task.Paths) != 1 || task.Paths[0] != "**" {
		t.Errorf("expected paths [**], got %+v", task.Paths)
	}
}

func TestScanDir_Caps(t *testing.T) {
	// Task cap test: generate > 200 tasks in a single dir
	tmpDir := t.TempDir()

	var sb strings.Builder
	sb.WriteString("from inspect_ai import task\n")
	for i := 0; i < 210; i++ {
		sb.WriteString(fmt.Sprintf("@task\ndef task_%03d():\n    pass\n", i))
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "many_tasks.py"), []byte(sb.String()), 0600); err != nil {
		t.Fatalf("failed to write many_tasks.py: %v", err)
	}

	res, err := ScanDir(tmpDir)
	if err != nil {
		t.Fatalf("ScanDir failed: %v", err)
	}

	if len(res.Manifest.Tasks) != MaxScanTasks {
		t.Fatalf("expected tasks capped at %d, got %d", MaxScanTasks, len(res.Manifest.Tasks))
	}
	if !res.TaskCapHit {
		t.Errorf("expected TaskCapHit to be true")
	}
	foundCapNote := false
	for _, n := range res.Notes {
		if strings.Contains(n, "task cap reached") {
			foundCapNote = true
			break
		}
	}
	if !foundCapNote {
		t.Errorf("expected task cap note in notes: %+v", res.Notes)
	}

	// File cap test: generate > 5,000 files in tar
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	// Add one task file first so it is extracted before the cap
	taskFileContent := []byte("from inspect_ai import task\n\n@task\ndef capped_task():\n    pass\n")
	tw.WriteHeader(&tar.Header{
		Name: "evals/a_task.py",
		Mode: 0644,
		Size: int64(len(taskFileContent)),
	})
	tw.Write(taskFileContent)

	for i := 0; i < 5005; i++ {
		content := []byte("from inspect_ai import task\n")
		hdr := &tar.Header{
			Name: fmt.Sprintf("evals/file_%05d.py", i),
			Mode: 0644,
			Size: int64(len(content)),
		}
		tw.WriteHeader(hdr)
		tw.Write(content)
	}
	tw.Close()

	resTar, err := ExtractAndScanTar(tarBuf.Bytes())
	if err != nil {
		t.Fatalf("ExtractAndScanTar failed: %v", err)
	}
	if !resTar.FileCapHit {
		t.Errorf("expected FileCapHit to be true")
	}
	foundFileCapNote := false
	for _, n := range resTar.Notes {
		if strings.Contains(n, "file cap reached") {
			foundFileCapNote = true
			break
		}
	}
	if !foundFileCapNote {
		t.Errorf("expected file cap note in notes: %+v", resTar.Notes)
	}
}

func TestExtractAndScanTar_Success(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	pyContent := []byte("from inspect_ai import task\n\n@task\ndef sample():\n    pass\n")
	hdr := &tar.Header{
		Name: "evals/sample.py",
		Mode: 0644,
		Size: int64(len(pyContent)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("failed to write tar header: %v", err)
	}
	if _, err := tw.Write(pyContent); err != nil {
		t.Fatalf("failed to write tar body: %v", err)
	}
	tw.Close()

	res, err := ExtractAndScanTar(buf.Bytes())
	if err != nil {
		t.Fatalf("ExtractAndScanTar failed: %v", err)
	}
	if len(res.Manifest.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(res.Manifest.Tasks))
	}
	if res.Manifest.Tasks[0].ID != "evals/sample" {
		t.Errorf("expected id evals/sample, got %s", res.Manifest.Tasks[0].ID)
	}
}

func TestExtractAndScanTar_EmptyArchive(t *testing.T) {
	// Empty tar (0 bytes) must return ErrNoInspectTasksFound
	_, err := ExtractAndScanTar([]byte{})
	if err != ErrNoInspectTasksFound {
		t.Fatalf("expected ErrNoInspectTasksFound for 0-byte archive, got: %v", err)
	}
}

func TestExtractAndScanTar_NoTasks(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	pyContent := []byte("def not_inspect():\n    pass\n")
	hdr := &tar.Header{
		Name: "evals/other.py",
		Mode: 0644,
		Size: int64(len(pyContent)),
	}
	tw.WriteHeader(hdr)
	tw.Write(pyContent)
	tw.Close()

	_, err := ExtractAndScanTar(buf.Bytes())
	if err != ErrNoInspectTasksFound {
		t.Fatalf("expected ErrNoInspectTasksFound when no tasks in archive, got: %v", err)
	}
}

func TestExtractAndScanTar_PathTraversal(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	hdr := &tar.Header{
		Name: "../evil.py",
		Mode: 0644,
		Size: 4,
	}
	tw.WriteHeader(hdr)
	tw.Write([]byte("evil"))
	tw.Close()

	_, err := ExtractAndScanTar(buf.Bytes())
	if err == nil || !strings.Contains(err.Error(), "path traversal") {
		t.Fatalf("expected path traversal error, got: %v", err)
	}
}
