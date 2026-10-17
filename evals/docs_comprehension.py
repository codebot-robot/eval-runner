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

"""Documentation comprehension evaluation tasks for eval-runner."""

from pathlib import Path
from inspect_ai import Task, task
from inspect_ai.dataset import FieldSpec, json_dataset
from inspect_ai.scorer import choice
from inspect_ai.solver import multiple_choice, system_message

# Recommended default model when running with an API key
DEFAULT_MODEL = "google/gemini-3.5-flash-lite"

CURRENT_DIR = Path(__file__).resolve().parent
REPO_ROOT = CURRENT_DIR.parent
DATASET_PATH = CURRENT_DIR / "datasets" / "readme_questions.jsonl"


def _resolve_model():
    """Returns the active model from CLI or environment if specified, else DEFAULT_MODEL."""
    try:
        from inspect_ai.model._model import active_model

        m = active_model()
        if m is not None and str(m) != "none/none":
            return m
    except Exception:
        pass
    return DEFAULT_MODEL


def _read_documentation() -> str:
    """Reads the repository documentation files at run time.

    Fails loudly with FileNotFoundError if documentation files are missing.
    """
    readme_path = REPO_ROOT / "README.md"
    dev_path = REPO_ROOT / "docs" / "development.md"

    readme_content = readme_path.read_text(encoding="utf-8")
    dev_content = dev_path.read_text(encoding="utf-8")

    return f"""The following documents describe the eval-runner project.

=== File: README.md ===
{readme_content}

=== File: docs/development.md ===
{dev_content}
"""


def _load_dataset():
    """Loads the comprehension questions dataset."""
    return json_dataset(
        json_file=str(DATASET_PATH),
        sample_fields=FieldSpec(
            input="input",
            choices="choices",
            target="target",
            metadata=["source", "evidence"],
        ),
    )


@task
def readme_comprehension() -> Task:
    """Evaluates reader comprehension of eval-runner documentation."""
    docs = _read_documentation()
    sys_prompt = f"""You are answering multiple-choice questions about the eval-runner project based solely on the provided documents.
Answer only from the provided documents. If an answer cannot be determined from the documents, choose the option stating that it is not stated or not mentioned. Do not assume or extrapolate anything not explicitly stated in the documents.

{docs}"""

    return Task(
        name="docs/readme_comprehension",
        model=_resolve_model(),
        dataset=_load_dataset(),
        solver=[
            system_message(sys_prompt),
            multiple_choice(),
        ],
        scorer=choice(),
    )


@task
def readme_comprehension_blind() -> Task:
    """Blind control task: evaluates answers without providing documentation to test for pretraining leakage."""
    sys_prompt = "You are answering multiple-choice questions about the eval-runner repository."

    return Task(
        name="docs/readme_comprehension_blind",
        model=_resolve_model(),
        dataset=_load_dataset(),
        solver=[
            system_message(sys_prompt),
            multiple_choice(),
        ],
        scorer=choice(),
    )
