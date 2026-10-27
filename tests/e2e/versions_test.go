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

package e2e

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentSandboxVersionMatchesVersionsScript(t *testing.T) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("failed to determine git root: %v", err)
	}
	gitRoot := strings.TrimSpace(string(out))

	versionsPath := filepath.Join(gitRoot, "dev", "tasks", "versions.sh")
	f, err := os.Open(versionsPath)
	if err != nil {
		t.Fatalf("failed to open %s: %v", versionsPath, err)
	}
	defer f.Close()

	var scriptVersion string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "AGENT_SANDBOX_VERSION=") {
			scriptVersion = strings.Trim(strings.TrimPrefix(line, "AGENT_SANDBOX_VERSION="), `"'`)
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("failed reading %s: %v", versionsPath, err)
	}

	if scriptVersion == "" {
		t.Fatalf("AGENT_SANDBOX_VERSION not found in %s", versionsPath)
	}

	if scriptVersion != AgentSandboxVersion {
		t.Errorf("AGENT_SANDBOX_VERSION in %s (%s) does not match AgentSandboxVersion in harness.go (%s)",
			versionsPath, scriptVersion, AgentSandboxVersion)
	}
}
