# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Shared helpers for dev/tasks Python automation scripts."""

import json
import os
import shlex
import subprocess
import sys
from pathlib import Path
from typing import Any, Dict, List, Optional

CONTROLLER_SERVICE_ACCOUNT = "eval-runner-controller"
UI_SERVICE_ACCOUNT = "eval-runner-ui"


class Step:
    """Prints a numbered heading for each step."""

    count = 0

    def __init__(self, title: str):
        Step.count += 1
        print(f"\n==> [{Step.count}] {title}", flush=True)


class Runner:
    """Runs external commands, honouring --dry-run and an optional dedicated KUBECONFIG."""

    def __init__(self, dry_run: bool, kubeconfig: Optional[Path] = None):
        self.dry_run = dry_run
        self.env = dict(os.environ)
        if kubeconfig is not None:
            self.env["KUBECONFIG"] = str(kubeconfig)
        # The deploy task pins ap via go run; it needs the toolchain to be free to download.
        self.env.setdefault("GOTOOLCHAIN", "auto")

    def show(
        self,
        command: List[str],
        env_overrides: Optional[Dict[str, str]] = None,
        stdin: Optional[str] = None,
    ) -> None:
        prefix = "".join(f"{k}={shlex.quote(v)} " for k, v in (env_overrides or {}).items())
        if stdin is not None:
            print(f"    $ {prefix}{shlex.join(command)} << 'EOF'\n{stdin.rstrip()}\nEOF", flush=True)
        else:
            print(f"    $ {prefix}{shlex.join(command)}", flush=True)

    def run(
        self,
        command: List[str],
        *,
        check: bool = True,
        capture: bool = False,
        quiet: bool = False,
        mutating: bool = True,
        env_overrides: Optional[Dict[str, str]] = None,
        stdin: Optional[str] = None,
    ) -> subprocess.CompletedProcess:
        """Run command. In dry-run mode, mutating commands are printed but not run."""
        if not quiet:
            self.show(command, env_overrides=env_overrides, stdin=stdin)
        if self.dry_run and mutating:
            return subprocess.CompletedProcess(command, 0, stdout="", stderr="")
        env = dict(self.env)
        env.update(env_overrides or {})
        result = subprocess.run(
            command,
            env=env,
            input=stdin,
            text=True,
            capture_output=capture,
            check=False,
        )
        if check and result.returncode != 0:
            if capture:
                sys.stderr.write(result.stderr)
            raise SystemExit(
                f"error: command failed with exit code {result.returncode}: {shlex.join(command)}"
            )
        return result

    def output(self, command: List[str], *, quiet: bool = True) -> str:
        """Run a read-only command and return stdout (also in dry-run mode)."""
        return self.run(command, capture=True, quiet=quiet, mutating=False).stdout.strip()


def gcloud_json(runner: Runner, command: List[str]) -> Optional[Any]:
    """Execute a gcloud command returning parsed JSON output, or None if empty."""
    output_text = runner.output(command + ["--format=json"])
    return json.loads(output_text) if output_text else None


def whoami(runner: Runner, project: str) -> str:
    """Return the active gcloud account, or exit with instructions."""
    account = runner.output(
        ["gcloud", "auth", "list", "--filter=status:ACTIVE", "--format=value(account)"]
    )
    if not account:
        raise SystemExit(
            "error: no active gcloud account. Run `gcloud auth login` (and `gcloud auth application-default login` "
            "if you also plan to run the e2e tests), then re-run this script."
        )
    account = account.splitlines()[0].strip()
    print(f"    authenticated as {account}")
    print(f"    project        {project}")
    return account


def project_number(runner: Runner, project: str) -> str:
    """Return the numeric project number for a GCP project ID."""
    number = runner.output(
        ["gcloud", "projects", "describe", project, "--format=value(projectNumber)"]
    )
    if not number:
        raise SystemExit(f"error: failed to determine project number for project {project}")
    return number


def permission_hint(account: str, project: str, error_message: str) -> str:
    """Format a helpful remediation hint for IAM permission denial errors."""
    detail = error_message.strip().splitlines()[-1] if error_message.strip() else "(no detail)"
    return (
        f"\n    Permission denied while changing bucket IAM as {account}.\n"
        f"    If you can administer this project, you may want to run\n"
        f"      gcloud projects add-iam-policy-binding {project} \\\n"
        f"        --member=\"user:{account}\" --role=\"roles/storage.admin\"\n"
        f"    then re-run this script. Otherwise ask a project owner to run the command above\n"
        f"    or the failing command. gcloud said:\n      {detail}\n"
    )
