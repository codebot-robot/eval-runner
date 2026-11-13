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

package terminalbench

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/eval-runner/pkg/artifacts"
)

type taskFixtureEnv struct {
	temporaryDirectory      string
	workingDirectory        string
	taskDirectory           string
	instructionContent      string
	logsDirectory           string
	outputDirectory         string
	targetSolutionDirectory string
	targetTestsDirectory    string
	runTaskScript           string
}

func newTaskFixtureEnv(t *testing.T, taskName string, testScriptBody string, instructionBody string) *taskFixtureEnv {
	t.Helper()

	temporaryDirectory := t.TempDir()
	workingDirectory := filepath.Join(temporaryDirectory, "work")
	if err := os.MkdirAll(workingDirectory, 0755); err != nil {
		t.Fatalf("failed to create working directory: %v", err)
	}

	taskDirectory := filepath.Join(temporaryDirectory, taskName)
	solutionDirectory := filepath.Join(taskDirectory, "solution")
	testsDirectory := filepath.Join(taskDirectory, "tests")
	if err := os.MkdirAll(solutionDirectory, 0755); err != nil {
		t.Fatalf("failed to create solution directory: %v", err)
	}
	if err := os.MkdirAll(testsDirectory, 0755); err != nil {
		t.Fatalf("failed to create tests directory: %v", err)
	}

	solveScript := "#!/bin/bash\necho 'running solution'\n"
	if err := os.WriteFile(filepath.Join(solutionDirectory, "solve.sh"), []byte(solveScript), 0755); err != nil {
		t.Fatalf("failed to write solve.sh: %v", err)
	}

	if testScriptBody != "" {
		if err := os.WriteFile(filepath.Join(testsDirectory, "test.sh"), []byte(testScriptBody), 0755); err != nil {
			t.Fatalf("failed to write test.sh: %v", err)
		}
	}

	if instructionBody != "" {
		if err := os.WriteFile(filepath.Join(taskDirectory, "instruction.md"), []byte(instructionBody), 0644); err != nil {
			t.Fatalf("failed to write instruction.md: %v", err)
		}
	}

	logsDirectory := filepath.Join(temporaryDirectory, "logs")
	outputDirectory := filepath.Join(temporaryDirectory, "out")
	targetSolutionDirectory := filepath.Join(temporaryDirectory, "solution")
	targetTestsDirectory := filepath.Join(temporaryDirectory, "tests")

	runTaskScript, err := filepath.Abs("run-task.sh")
	if err != nil {
		t.Fatalf("failed to resolve run-task.sh path: %v", err)
	}

	return &taskFixtureEnv{
		temporaryDirectory:      temporaryDirectory,
		workingDirectory:        workingDirectory,
		taskDirectory:           taskDirectory,
		instructionContent:      instructionBody,
		logsDirectory:           logsDirectory,
		outputDirectory:         outputDirectory,
		targetSolutionDirectory: targetSolutionDirectory,
		targetTestsDirectory:    targetTestsDirectory,
		runTaskScript:           runTaskScript,
	}
}

func (env *taskFixtureEnv) run(mode string, taskName string) ([]byte, error) {
	command := exec.Command("bash", env.runTaskScript, taskName, mode, env.workingDirectory)
	command.Dir = env.temporaryDirectory
	command.Env = append(
		os.Environ(),
		"TASK_DIRECTORY="+env.temporaryDirectory,
		"LOGS_DIRECTORY="+env.logsDirectory,
		"OUT_DIRECTORY="+env.outputDirectory,
		"SOLUTION_DIRECTORY="+env.targetSolutionDirectory,
		"TESTS_DIRECTORY="+env.targetTestsDirectory,
	)
	return command.CombinedOutput()
}

func (env *taskFixtureEnv) readResults(t *testing.T) artifacts.ResultsFile {
	t.Helper()
	resultsPath := filepath.Join(env.outputDirectory, "results.json")
	resultsBytes, err := os.ReadFile(resultsPath)
	if err != nil {
		t.Fatalf("failed to read results.json: %v", err)
	}
	var results artifacts.ResultsFile
	if err := json.Unmarshal(resultsBytes, &results); err != nil {
		t.Fatalf("failed to parse results.json: %v\ncontent: %s", err, string(resultsBytes))
	}
	return results
}

func runScriptInEnvironment(t *testing.T, taskName string, testScriptBody string) artifacts.ResultsFile {
	t.Helper()

	env := newTaskFixtureEnv(t, taskName, testScriptBody, "Oracle test instruction\n")
	output, err := env.run("oracle", taskName)
	if err != nil {
		t.Fatalf("run-task.sh failed: %v\noutput: %s", err, string(output))
	}

	return env.readResults(t)
}

func TestRunTask(t *testing.T) {
	testCases := []struct {
		name            string
		testScript      string
		wantPassed      bool
		wantScore       float64
		wantMessagePart string
	}{
		{
			name: "reward 1 passes",
			testScript: "#!/bin/bash\n" +
				"mkdir -p \"${LOGS_DIRECTORY}/verifier\"\n" +
				"echo 1 > \"${LOGS_DIRECTORY}/verifier/reward.txt\"\n",
			wantPassed: true,
			wantScore:  1.0,
		},
		{
			name: "reward 0 fails with verifier log",
			testScript: "#!/bin/bash\n" +
				"mkdir -p \"${LOGS_DIRECTORY}/verifier\"\n" +
				"echo 'assertion error in output'\n" +
				"echo 0 > \"${LOGS_DIRECTORY}/verifier/reward.txt\"\n",
			wantPassed:      false,
			wantScore:       0.0,
			wantMessagePart: "assertion error in output",
		},
		{
			name: "missing reward fails with message",
			testScript: "#!/bin/bash\n" +
				"echo 'verifier crashed before producing reward'\n" +
				"exit 1\n",
			wantPassed:      false,
			wantScore:       0.0,
			wantMessagePart: "did not write reward",
		},
		{
			name: "non-numeric reward fails",
			testScript: "#!/bin/bash\n" +
				"mkdir -p \"${LOGS_DIRECTORY}/verifier\"\n" +
				"echo 'not-a-number' > \"${LOGS_DIRECTORY}/verifier/reward.txt\"\n",
			wantPassed:      false,
			wantScore:       0.0,
			wantMessagePart: "non-numeric reward",
		},
		{
			name: "ansi color codes stripped cleanly in results JSON",
			testScript: "#!/bin/bash\n" +
				"mkdir -p \"${LOGS_DIRECTORY}/verifier\"\n" +
				"printf '\\033[31mfailed with color\\033[0m\\n'\n" +
				"echo 0 > \"${LOGS_DIRECTORY}/verifier/reward.txt\"\n",
			wantPassed:      false,
			wantScore:       0.0,
			wantMessagePart: "failed with color",
		},
		{
			name: "reward.json preferred over reward.txt",
			testScript: "#!/bin/bash\n" +
				"mkdir -p \"${LOGS_DIRECTORY}/verifier\"\n" +
				"echo 0 > \"${LOGS_DIRECTORY}/verifier/reward.txt\"\n" +
				"echo '{\"reward\": 1.0}' > \"${LOGS_DIRECTORY}/verifier/reward.json\"\n",
			wantPassed: true,
			wantScore:  1.0,
		},
		{
			name: "reward.json multi-metric dict parses reward key",
			testScript: "#!/bin/bash\n" +
				"mkdir -p \"${LOGS_DIRECTORY}/verifier\"\n" +
				"echo '{\"accuracy\": 0.5, \"reward\": 1}' > \"${LOGS_DIRECTORY}/verifier/reward.json\"\n",
			wantPassed: true,
			wantScore:  1.0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			results := runScriptInEnvironment(t, "sample-task", tc.testScript)
			if len(results.Samples) != 1 {
				t.Fatalf("expected 1 sample, got %d", len(results.Samples))
			}
			sample := results.Samples[0]
			if sample.Passed == nil || *sample.Passed != tc.wantPassed {
				t.Errorf("Passed = %v, want %v (message: %s)", sample.Passed, tc.wantPassed, sample.Message)
			}
			if sample.Score == nil || *sample.Score != tc.wantScore {
				t.Errorf("Score = %v, want %v", sample.Score, tc.wantScore)
			}
			if tc.wantMessagePart != "" && !strings.Contains(sample.Message, tc.wantMessagePart) {
				t.Errorf("Message %q does not contain %q", sample.Message, tc.wantMessagePart)
			}
			if tc.wantPassed && sample.Message != "" {
				t.Errorf("expected empty message on success, got %q", sample.Message)
			}
		})
	}
}

func TestRunTask_Prepare(t *testing.T) {
	expectedInstruction := "## Task Instructions\nFix the bug in the code.\n"
	env := newTaskFixtureEnv(t, "sample-task", "#!/bin/bash\necho 1 > reward.txt\n", expectedInstruction)

	out, err := env.run("prepare", "sample-task")
	if err != nil {
		t.Fatalf("prepare failed: %v\noutput: %s", err, string(out))
	}

	// 1. Instruction written to OUT_DIRECTORY/instruction.md
	instructionPath := filepath.Join(env.outputDirectory, "instruction.md")
	instructionBytes, err := os.ReadFile(instructionPath)
	if err != nil {
		t.Fatalf("expected instruction.md to exist: %v", err)
	}
	if string(instructionBytes) != expectedInstruction {
		t.Errorf("instruction.md = %q, want %q", string(instructionBytes), expectedInstruction)
	}

	// 2. Tests installed to TESTS_DIRECTORY
	installedTestScript := filepath.Join(env.targetTestsDirectory, "test.sh")
	testBytes, err := os.ReadFile(installedTestScript)
	if err != nil {
		t.Fatalf("expected test.sh in tests directory: %v", err)
	}
	if !strings.Contains(string(testBytes), "reward.txt") {
		t.Errorf("test.sh content unexpected: %q", string(testBytes))
	}

	// 3. Solution directory must NOT be present
	if _, err := os.Stat(env.targetSolutionDirectory); !os.IsNotExist(err) {
		t.Errorf("expected solution directory to not exist, but stat returned: %v", err)
	}

	// 4. results.json should NOT be written on successful prepare
	resultsPath := filepath.Join(env.outputDirectory, "results.json")
	if _, err := os.Stat(resultsPath); !os.IsNotExist(err) {
		t.Errorf("expected results.json to not exist after successful prepare")
	}
}

func TestRunTask_PrepareFailure(t *testing.T) {
	env := newTaskFixtureEnv(t, "sample-task", "#!/bin/bash\necho 1\n", "instruction")

	// Run prepare with a nonexistent task
	out, err := env.run("prepare", "nonexistent-task")
	if err == nil {
		t.Fatalf("expected prepare for nonexistent task to fail, but succeeded\noutput: %s", string(out))
	}

	// Failure must write failing results.json and exit non-zero
	results := env.readResults(t)
	if len(results.Samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(results.Samples))
	}
	sample := results.Samples[0]
	if sample.Passed == nil || *sample.Passed {
		t.Errorf("expected passed: false, got %v", sample.Passed)
	}
	if !strings.Contains(sample.Message, "task directory not found") {
		t.Errorf("expected message to mention 'task directory not found', got %q", sample.Message)
	}
}

func TestRunTask_VerifyAfterPrepare(t *testing.T) {
	t.Run("passing reward 1", func(t *testing.T) {
		testScript := "#!/bin/bash\n" +
			"mkdir -p \"${LOGS_DIRECTORY}/verifier\"\n" +
			"echo 1 > \"${LOGS_DIRECTORY}/verifier/reward.txt\"\n"
		env := newTaskFixtureEnv(t, "sample-task", testScript, "Test instruction")

		// 1. Run prepare
		prepOut, err := env.run("prepare", "sample-task")
		if err != nil {
			t.Fatalf("prepare failed: %v\noutput: %s", err, string(prepOut))
		}

		// Solution must not exist after prepare
		if _, err := os.Stat(env.targetSolutionDirectory); !os.IsNotExist(err) {
			t.Fatalf("expected solution directory to not exist after prepare")
		}

		// 2. Run verify
		verifyOut, err := env.run("verify", "sample-task")
		if err != nil {
			t.Fatalf("verify failed: %v\noutput: %s", err, string(verifyOut))
		}

		results := env.readResults(t)
		if len(results.Samples) != 1 {
			t.Fatalf("expected 1 sample, got %d", len(results.Samples))
		}
		sample := results.Samples[0]
		if sample.Passed == nil || !*sample.Passed {
			t.Errorf("expected passed: true, got %v (message: %s)", sample.Passed, sample.Message)
		}
		if sample.Score == nil || *sample.Score != 1.0 {
			t.Errorf("expected score: 1.0, got %v", sample.Score)
		}

		// Solution must not exist after verify
		if _, err := os.Stat(env.targetSolutionDirectory); !os.IsNotExist(err) {
			t.Errorf("expected solution directory to not exist after verify")
		}
	})

	t.Run("failing reward 0", func(t *testing.T) {
		testScript := "#!/bin/bash\n" +
			"mkdir -p \"${LOGS_DIRECTORY}/verifier\"\n" +
			"echo 'verification failure details'\n" +
			"echo 0 > \"${LOGS_DIRECTORY}/verifier/reward.txt\"\n"
		env := newTaskFixtureEnv(t, "sample-task", testScript, "Test instruction")

		// 1. Run prepare
		prepOut, err := env.run("prepare", "sample-task")
		if err != nil {
			t.Fatalf("prepare failed: %v\noutput: %s", err, string(prepOut))
		}

		// 2. Run verify
		verifyOut, err := env.run("verify", "sample-task")
		if err != nil {
			t.Fatalf("verify exited non-zero on reward 0: %v\noutput: %s", err, string(verifyOut))
		}

		results := env.readResults(t)
		if len(results.Samples) != 1 {
			t.Fatalf("expected 1 sample, got %d", len(results.Samples))
		}
		sample := results.Samples[0]
		if sample.Passed == nil || *sample.Passed {
			t.Errorf("expected passed: false, got %v", sample.Passed)
		}
		if sample.Score == nil || *sample.Score != 0.0 {
			t.Errorf("expected score: 0.0, got %v", sample.Score)
		}
		if !strings.Contains(sample.Message, "verification failure details") {
			t.Errorf("expected message to contain 'verification failure details', got %q", sample.Message)
		}
	})
}

func TestRunTask_VerifyWithoutPrepareFailsClearly(t *testing.T) {
	env := newTaskFixtureEnv(t, "sample-task", "#!/bin/bash\necho 1\n", "Test instruction")

	// Call verify directly without running prepare
	out, err := env.run("verify", "sample-task")
	if err == nil {
		t.Fatalf("expected verify without prepare to fail, but succeeded\noutput: %s", string(out))
	}

	if !strings.Contains(string(out), "prepare") {
		t.Errorf("expected output to mention 'prepare', got: %s", string(out))
	}

	results := env.readResults(t)
	if len(results.Samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(results.Samples))
	}
	sample := results.Samples[0]
	if sample.Passed == nil || *sample.Passed {
		t.Errorf("expected passed: false, got %v", sample.Passed)
	}
	if !strings.Contains(sample.Message, "prepare") {
		t.Errorf("expected message to mention 'prepare', got %q", sample.Message)
	}
}

func TestRunTask_FetchFailureNoTools(t *testing.T) {
	temporaryDirectory := t.TempDir()

	restrictedBinDirectory := filepath.Join(temporaryDirectory, "bin")
	if err := os.MkdirAll(restrictedBinDirectory, 0755); err != nil {
		t.Fatalf("failed to create bin directory: %v", err)
	}

	// Symlink only essential shell utilities, intentionally excluding git, curl, wget, python3, apt-get, apk
	essentialUtilities := []string{"bash", "sh", "awk", "cat", "rm", "mkdir", "tail", "grep", "sed", "chmod", "printf", "echo", "test"}
	for _, utility := range essentialUtilities {
		path, err := exec.LookPath(utility)
		if err == nil {
			_ = os.Symlink(path, filepath.Join(restrictedBinDirectory, utility))
		}
	}

	logsDirectory := filepath.Join(temporaryDirectory, "logs")
	outputDirectory := filepath.Join(temporaryDirectory, "out")
	targetSolutionDirectory := filepath.Join(temporaryDirectory, "solution")
	targetTestsDirectory := filepath.Join(temporaryDirectory, "tests")

	runTaskScript, err := filepath.Abs("run-task.sh")
	if err != nil {
		t.Fatalf("failed to resolve run-task.sh path: %v", err)
	}

	command := exec.Command(filepath.Join(restrictedBinDirectory, "bash"), runTaskScript, "sample-task", "oracle", "/app")
	command.Dir = temporaryDirectory
	command.Env = []string{
		"PATH=" + restrictedBinDirectory,
		"PINNED_COMMIT=fake-commit",
		"LOGS_DIRECTORY=" + logsDirectory,
		"OUT_DIRECTORY=" + outputDirectory,
		"SOLUTION_DIRECTORY=" + targetSolutionDirectory,
		"TESTS_DIRECTORY=" + targetTestsDirectory,
	}

	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run-task.sh exited with non-zero status: %v\noutput: %s", err, string(output))
	}

	resultsPath := filepath.Join(outputDirectory, "results.json")
	resultsBytes, err := os.ReadFile(resultsPath)
	if err != nil {
		t.Fatalf("expected results.json to exist after fetch failure, but read failed: %v", err)
	}

	var results artifacts.ResultsFile
	if err := json.Unmarshal(resultsBytes, &results); err != nil {
		t.Fatalf("failed to parse results.json: %v\ncontent: %s", err, string(resultsBytes))
	}

	if len(results.Samples) != 1 {
		t.Fatalf("expected 1 sample in results.json, got %d", len(results.Samples))
	}
	sample := results.Samples[0]
	if sample.Passed == nil || *sample.Passed {
		t.Errorf("expected passed: false, got %v", sample.Passed)
	}
	if !strings.Contains(sample.Message, "failed to fetch repository") {
		t.Errorf("expected message to mention 'failed to fetch repository', got %q", sample.Message)
	}
}

func TestRunTask_PinnedCommitMissingFails(t *testing.T) {
	temporaryDirectory := t.TempDir()
	logsDirectory := filepath.Join(temporaryDirectory, "logs")
	outputDirectory := filepath.Join(temporaryDirectory, "out")
	targetSolutionDirectory := filepath.Join(temporaryDirectory, "solution")
	targetTestsDirectory := filepath.Join(temporaryDirectory, "tests")

	// Copy run-task.sh to temporaryDirectory without version.sh so PINNED_COMMIT cannot be resolved
	runTaskSourcePath, err := filepath.Abs("run-task.sh")
	if err != nil {
		t.Fatalf("failed to resolve run-task.sh path: %v", err)
	}
	runTaskScript := filepath.Join(temporaryDirectory, "run-task.sh")
	scriptContent, err := os.ReadFile(runTaskSourcePath)
	if err != nil {
		t.Fatalf("failed to read run-task.sh: %v", err)
	}
	if err := os.WriteFile(runTaskScript, scriptContent, 0755); err != nil {
		t.Fatalf("failed to copy run-task.sh: %v", err)
	}

	command := exec.Command("bash", runTaskScript, "sample-task", "oracle", "/app")
	command.Dir = temporaryDirectory
	command.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"LOGS_DIRECTORY=" + logsDirectory,
		"OUT_DIRECTORY=" + outputDirectory,
		"SOLUTION_DIRECTORY=" + targetSolutionDirectory,
		"TESTS_DIRECTORY=" + targetTestsDirectory,
	}

	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run-task.sh exited with non-zero status: %v\noutput: %s", err, string(output))
	}

	resultsPath := filepath.Join(outputDirectory, "results.json")
	resultsBytes, err := os.ReadFile(resultsPath)
	if err != nil {
		t.Fatalf("expected results.json to exist, but read failed: %v", err)
	}

	var results artifacts.ResultsFile
	if err := json.Unmarshal(resultsBytes, &results); err != nil {
		t.Fatalf("failed to parse results.json: %v\ncontent: %s", err, string(resultsBytes))
	}

	if len(results.Samples) != 1 {
		t.Fatalf("expected 1 sample in results.json, got %d", len(results.Samples))
	}
	sample := results.Samples[0]
	if sample.Passed == nil || *sample.Passed {
		t.Errorf("expected passed: false, got %v", sample.Passed)
	}
	if !strings.Contains(sample.Message, "PINNED_COMMIT not set: version.sh not found") {
		t.Errorf("expected message to mention 'PINNED_COMMIT not set: version.sh not found', got %q", sample.Message)
	}
}
