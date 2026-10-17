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

"""Unit tests and drift guard for documentation comprehension dataset."""

import json
from pathlib import Path
import unittest

from docs_comprehension import _read_documentation

DATASET_PATH = Path(__file__).resolve().parent / "datasets" / "readme_questions.jsonl"


def validate_dataset(samples: list[dict], docs: str) -> None:
    """Validates that every sample contains an evidence string found in the injected docs.

    Raises ValueError if a sample is missing evidence or the evidence is not in docs.
    """
    for i, sample in enumerate(samples):
        evidence = sample.get("evidence")
        if not evidence or not isinstance(evidence, str) or not evidence.strip():
            raise ValueError(
                f"Sample #{i + 1} ({sample.get('input', 'unknown')}) is missing a valid 'evidence' field."
            )
        if evidence not in docs:
            raise ValueError(
                f"Sample #{i + 1} ({sample.get('input', 'unknown')}) evidence string not found in documentation:\n"
                f"  Missing evidence: {evidence!r}"
            )


class TestDocsDrift(unittest.TestCase):
    """Verifies dataset evidence strings are present in repository documentation."""

    def setUp(self):
        self.docs = _read_documentation()
        self.assertTrue(DATASET_PATH.is_file(), f"Dataset file missing at {DATASET_PATH}")
        with open(DATASET_PATH, encoding="utf-8") as f:
            self.samples = [json.loads(line) for line in f if line.strip()]

    def test_all_dataset_samples_evidence_present(self):
        """Positive test: Every sample in the dataset must have an evidence string present in docs."""
        self.assertGreaterEqual(len(self.samples), 20, "Expected at least 20 questions in dataset")
        validate_dataset(self.samples, self.docs)

    def test_missing_evidence_field_fails(self):
        """Negative unit check: Samples missing the evidence field must fail validation."""
        bad_sample = [
            {
                "input": "Sample without evidence",
                "choices": ["A", "B", "C", "D"],
                "target": "A",
                "source": "README.md",
            }
        ]
        with self.assertRaises(ValueError) as ctx:
            validate_dataset(bad_sample, self.docs)
        self.assertIn("missing a valid 'evidence' field", str(ctx.exception))

    def test_empty_evidence_field_fails(self):
        """Negative unit check: Samples with an empty evidence string must fail validation."""
        bad_sample = [
            {
                "input": "Sample with empty evidence",
                "choices": ["A", "B", "C", "D"],
                "target": "A",
                "source": "README.md",
                "evidence": "   ",
            }
        ]
        with self.assertRaises(ValueError) as ctx:
            validate_dataset(bad_sample, self.docs)
        self.assertIn("missing a valid 'evidence' field", str(ctx.exception))

    def test_evidence_not_in_docs_fails(self):
        """Negative unit check: Samples with evidence not appearing in documentation must fail validation."""
        bad_sample = [
            {
                "input": "Sample with hallucinated evidence",
                "choices": ["A", "B", "C", "D"],
                "target": "A",
                "source": "README.md",
                "evidence": "THIS_STRING_IS_DEFINITELY_NOT_IN_README_OR_DEVELOPMENT_MD_XYZ123",
            }
        ]
        with self.assertRaises(ValueError) as ctx:
            validate_dataset(bad_sample, self.docs)
        self.assertIn("not found in documentation", str(ctx.exception))


if __name__ == "__main__":
    unittest.main()
