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

func runScriptInEnvironment(t *testing.T, taskName string, testScriptBody string) artifacts.ResultsFile {
	t.Helper()

	temporaryDirectory := t.TempDir()
	workingDirectory := filepath.Join(temporaryDirectory, "work")
	if err := os.MkdirAll(workingDirectory, 0755); err != nil {
		t.Fatalf("failed to create working directory: %v", err)
	}

	taskDirectory := filepath.Join(temporaryDirectory, "tasks", taskName)
	solutionDirectory := filepath.Join(taskDirectory, "solution")
	testsDirectory := filepath.Join(taskDirectory, "tests")
	if err := os.MkdirAll(solutionDirectory, 0755); err != nil {
		t.Fatalf("failed to create solution directory: %v", err)
	}
	if err := os.MkdirAll(testsDirectory, 0755); err != nil {
		t.Fatalf("failed to create tests directory: %v", err)
	}

	if err := os.WriteFile(filepath.Join(taskDirectory, "WORKDIR"), []byte(workingDirectory+"\n"), 0644); err != nil {
		t.Fatalf("failed to write WORKDIR: %v", err)
	}

	solveScript := "#!/bin/bash\necho 'running solution'\n"
	if err := os.WriteFile(filepath.Join(solutionDirectory, "solve.sh"), []byte(solveScript), 0755); err != nil {
		t.Fatalf("failed to write solve.sh: %v", err)
	}

	if err := os.WriteFile(filepath.Join(testsDirectory, "test.sh"), []byte(testScriptBody), 0755); err != nil {
		t.Fatalf("failed to write test.sh: %v", err)
	}

	logsDirectory := filepath.Join(temporaryDirectory, "logs")
	outputDirectory := filepath.Join(temporaryDirectory, "out")
	targetSolutionDirectory := filepath.Join(temporaryDirectory, "solution")
	targetTestsDirectory := filepath.Join(temporaryDirectory, "tests")

	runTaskScript, err := filepath.Abs("run-task.sh")
	if err != nil {
		t.Fatalf("failed to resolve run-task.sh path: %v", err)
	}

	command := exec.Command("bash", runTaskScript, taskName, "oracle")
	command.Dir = temporaryDirectory
	command.Env = append(
		os.Environ(),
		"LOGS_DIRECTORY="+logsDirectory,
		"OUT_DIRECTORY="+outputDirectory,
		"SOLUTION_DIRECTORY="+targetSolutionDirectory,
		"TESTS_DIRECTORY="+targetTestsDirectory,
	)

	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run-task.sh failed: %v\noutput: %s", err, string(output))
	}

	resultsPath := filepath.Join(outputDirectory, "results.json")
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
