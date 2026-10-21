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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
)

func TestScanInspectTasks_FixtureTree(t *testing.T) {
	tmpDir := t.TempDir()

	// .gitignore
	gitignoreContent := `
# Ignore decoy in gitignore
ignored_task.py
build/
`
	if err := os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte(gitignoreContent), 0644); err != nil {
		t.Fatalf("failed to write .gitignore: %v", err)
	}

	// Task file 1: evals/runbook.py (2 tasks)
	evalsDir := filepath.Join(tmpDir, "evals")
	if err := os.MkdirAll(evalsDir, 0755); err != nil {
		t.Fatalf("failed to mkdir evals: %v", err)
	}
	task1Content := `
from inspect_ai import task, Task

@task
def runbook_lookup() -> Task:
    pass

@task()
def escalation_policy():
    pass
`
	if err := os.WriteFile(filepath.Join(evalsDir, "runbook.py"), []byte(task1Content), 0644); err != nil {
		t.Fatalf("failed to write runbook.py: %v", err)
	}

	// Task file 2: bench/test_bench.py (1 task with inspect_ai.task)
	benchDir := filepath.Join(tmpDir, "bench")
	if err := os.MkdirAll(benchDir, 0755); err != nil {
		t.Fatalf("failed to mkdir bench: %v", err)
	}
	task2Content := `
import inspect_ai

@inspect_ai.task(name="bench_eval")
async def bench_eval():
    pass
`
	if err := os.WriteFile(filepath.Join(benchDir, "test_bench.py"), []byte(task2Content), 0644); err != nil {
		t.Fatalf("failed to write test_bench.py: %v", err)
	}

	// Decoy 1: evals/decoy.py (imports inspect_ai, but NO @task decorator)
	decoy1Content := `
from inspect_ai import Task

def helper_function():
    pass
`
	if err := os.WriteFile(filepath.Join(evalsDir, "decoy.py"), []byte(decoy1Content), 0644); err != nil {
		t.Fatalf("failed to write decoy.py: %v", err)
	}

	// Decoy 2: decoy_no_import.py (has @task decorator, but NO inspect_ai import)
	decoy2Content := `
@task
def fake_task():
    pass
`
	if err := os.WriteFile(filepath.Join(tmpDir, "decoy_no_import.py"), []byte(decoy2Content), 0644); err != nil {
		t.Fatalf("failed to write decoy_no_import.py: %v", err)
	}

	// Decoy 3: under .venv
	venvDir := filepath.Join(tmpDir, ".venv", "lib")
	if err := os.MkdirAll(venvDir, 0755); err != nil {
		t.Fatalf("failed to mkdir .venv: %v", err)
	}
	venvContent := `
from inspect_ai import task

@task
def venv_task():
    pass
`
	if err := os.WriteFile(filepath.Join(venvDir, "venv_task.py"), []byte(venvContent), 0644); err != nil {
		t.Fatalf("failed to write venv_task.py: %v", err)
	}

	// Decoy 4: ignored by .gitignore
	ignoredContent := `
from inspect_ai import task

@task
def ignored_task():
    pass
`
	if err := os.WriteFile(filepath.Join(evalsDir, "ignored_task.py"), []byte(ignoredContent), 0644); err != nil {
		t.Fatalf("failed to write ignored_task.py: %v", err)
	}

	res, err := ScanInspectTasks(tmpDir)
	if err != nil {
		t.Fatalf("ScanInspectTasks failed: %v", err)
	}

	if res.Manifest.Source != "inspect-scan" {
		t.Errorf("Expected source %q, got %q", "inspect-scan", res.Manifest.Source)
	}

	if len(res.Manifest.Tasks) != 3 {
		t.Fatalf("Expected 3 tasks, got %d: %+v", len(res.Manifest.Tasks), res.Manifest.Tasks)
	}

	// Check task 0: bench/bench_eval
	t0 := res.Manifest.Tasks[0]
	if t0.ID != "bench/bench_eval" || t0.Workdir != "bench" || t0.Spec != "test_bench.py@bench_eval" {
		t.Errorf("Unexpected task 0: %+v", t0)
	}
	if len(t0.Paths) != 1 || t0.Paths[0] != "bench/**" {
		t.Errorf("Unexpected task 0 paths: %v", t0.Paths)
	}
	if t0.Cohort != v1alpha1.DefaultCohort {
		t.Errorf("Expected cohort %q, got %q", v1alpha1.DefaultCohort, t0.Cohort)
	}
	if len(t0.Resources) != 0 {
		t.Errorf("Expected empty resources, got %v", t0.Resources)
	}

	// Check task 1: evals/escalation_policy
	t1 := res.Manifest.Tasks[1]
	if t1.ID != "evals/escalation_policy" || t1.Workdir != "evals" || t1.Spec != "runbook.py@escalation_policy" {
		t.Errorf("Unexpected task 1: %+v", t1)
	}
	if len(t1.Paths) != 1 || t1.Paths[0] != "evals/**" {
		t.Errorf("Unexpected task 1 paths: %v", t1.Paths)
	}

	// Check task 2: evals/runbook_lookup
	t2 := res.Manifest.Tasks[2]
	if t2.ID != "evals/runbook_lookup" || t2.Workdir != "evals" || t2.Spec != "runbook.py@runbook_lookup" {
		t.Errorf("Unexpected task 2: %+v", t2)
	}
	if len(t2.Paths) != 1 || t2.Paths[0] != "evals/**" {
		t.Errorf("Unexpected task 2 paths: %v", t2.Paths)
	}

	// Validate synthesized manifest passes ValidateManifest
	if err := ValidateManifest(res.Manifest); err != nil {
		t.Fatalf("Synthesized manifest failed ValidateManifest: %v", err)
	}
}

func TestScanInspectTasks_IDsStable(t *testing.T) {
	tmpDir := t.TempDir()

	for i := 1; i <= 5; i++ {
		subDir := filepath.Join(tmpDir, fmt.Sprintf("sub%d", i))
		_ = os.MkdirAll(subDir, 0755)
		content := fmt.Sprintf(`
from inspect_ai import task

@task
def task_%d():
    pass
`, i)
		_ = os.WriteFile(filepath.Join(subDir, "eval.py"), []byte(content), 0644)
	}

	res1, err := ScanInspectTasks(tmpDir)
	if err != nil {
		t.Fatalf("first scan failed: %v", err)
	}
	res2, err := ScanInspectTasks(tmpDir)
	if err != nil {
		t.Fatalf("second scan failed: %v", err)
	}

	if len(res1.Manifest.Tasks) != len(res2.Manifest.Tasks) {
		t.Fatalf("Task counts differ: %d vs %d", len(res1.Manifest.Tasks), len(res2.Manifest.Tasks))
	}

	for i := range res1.Manifest.Tasks {
		if res1.Manifest.Tasks[i].ID != res2.Manifest.Tasks[i].ID {
			t.Errorf("Task ID mismatch at %d: %q vs %q", i, res1.Manifest.Tasks[i].ID, res2.Manifest.Tasks[i].ID)
		}
	}
}

func TestScanInspectTasks_TaskCap(t *testing.T) {
	tmpDir := t.TempDir()

	// Generate 220 tasks in a single file
	var sb strings.Builder
	sb.WriteString("from inspect_ai import task\n")
	for i := 0; i < 220; i++ {
		sb.WriteString(fmt.Sprintf("@task\ndef task_%03d():\n    pass\n\n", i))
	}
	_ = os.WriteFile(filepath.Join(tmpDir, "many_tasks.py"), []byte(sb.String()), 0644)

	res, err := ScanInspectTasks(tmpDir)
	if err != nil {
		t.Fatalf("ScanInspectTasks failed: %v", err)
	}

	if len(res.Manifest.Tasks) != MaxScanTasks {
		t.Errorf("Expected %d tasks (capped), got %d", MaxScanTasks, len(res.Manifest.Tasks))
	}
	if !res.TaskCapHit {
		t.Errorf("Expected TaskCapHit to be true")
	}
	if res.CapMessage() != "task scan capped at 200 tasks" {
		t.Errorf("Expected cap message %q, got %q", "task scan capped at 200 tasks", res.CapMessage())
	}
}

func TestScanInspectTasks_FileCap(t *testing.T) {
	tmpDir := t.TempDir()

	// Generate 5005 dummy files
	for i := 0; i < MaxScanFiles+5; i++ {
		name := filepath.Join(tmpDir, fmt.Sprintf("file_%05d.txt", i))
		if err := os.WriteFile(name, []byte("x"), 0644); err != nil {
			t.Fatalf("failed to write dummy file: %v", err)
		}
	}

	res, err := ScanInspectTasks(tmpDir)
	if err != nil {
		t.Fatalf("ScanInspectTasks failed: %v", err)
	}

	if !res.FileCapHit {
		t.Errorf("Expected FileCapHit to be true")
	}
	if res.FilesCount < MaxScanFiles {
		t.Errorf("Expected FilesCount >= %d, got %d", MaxScanFiles, res.FilesCount)
	}
	if res.CapMessage() != "file scan capped at 5000 files" {
		t.Errorf("Expected cap message %q, got %q", "file scan capped at 5000 files", res.CapMessage())
	}
}

func TestScanInspectTasks_RootFile(t *testing.T) {
	tmpDir := t.TempDir()

	content := `
from inspect_ai import task

@task
def root_task():
    pass
`
	_ = os.WriteFile(filepath.Join(tmpDir, "eval.py"), []byte(content), 0644)

	res, err := ScanInspectTasks(tmpDir)
	if err != nil {
		t.Fatalf("ScanInspectTasks failed: %v", err)
	}

	if len(res.Manifest.Tasks) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(res.Manifest.Tasks))
	}

	t0 := res.Manifest.Tasks[0]
	if t0.ID != "root_task" {
		t.Errorf("Expected ID 'root_task', got %q", t0.ID)
	}
	if t0.Workdir != "." {
		t.Errorf("Expected Workdir '.', got %q", t0.Workdir)
	}
	if t0.Spec != "eval.py@root_task" {
		t.Errorf("Expected Spec 'eval.py@root_task', got %q", t0.Spec)
	}
	if len(t0.Paths) != 1 || t0.Paths[0] != "**" {
		t.Errorf("Expected Paths ['**'], got %v", t0.Paths)
	}
}
