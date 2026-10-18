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
import re
import unittest

from docs_comprehension import _read_documentation

DATASET_PATH = Path(__file__).resolve().parent / "datasets" / "readme_questions.jsonl"
ID_PATTERN = re.compile(r"^[a-z0-9]+(-[a-z0-9]+)*$")


def validate_dataset(samples: list[dict], docs: str) -> None:
    """Validates that every sample contains a unique, valid kebab-case 'id',
    an evidence string found in the injected docs, and that any optional
    'absent' strings do not appear in the injected docs.

    Raises ValueError if a sample is missing id or evidence, has invalid id format,
    duplicate ids, evidence is not in docs, or any 'absent' string appears in docs.
    """
    seen_ids = set()
    for i, sample in enumerate(samples):
        sample_id = sample.get("id")
        if not sample_id or not isinstance(sample_id, str) or not sample_id.strip():
            raise ValueError(
                f"Sample #{i + 1} ({sample.get('input', 'unknown')}) is missing a valid 'id' field."
            )
        if not ID_PATTERN.match(sample_id):
            raise ValueError(
                f"Sample #{i + 1} ({sample.get('input', 'unknown')}) has invalid id format: '{sample_id}'. Expected lowercase kebab-case (e.g. 'readme-tagline')."
            )
        if sample_id in seen_ids:
            raise ValueError(
                f"Sample #{i + 1} ({sample.get('input', 'unknown')}) has duplicate id: '{sample_id}'."
            )
        seen_ids.add(sample_id)

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
        absent = sample.get("absent")
        if absent is not None:
            if not isinstance(absent, list):
                raise ValueError(
                    f"Sample #{i + 1} ({sample.get('input', 'unknown')}) 'absent' field must be a list of strings."
                )
            for absent_str in absent:
                if not isinstance(absent_str, str) or not absent_str.strip():
                    raise ValueError(
                        f"Sample #{i + 1} ({sample.get('input', 'unknown')}) has an invalid absent string: {absent_str!r}."
                    )
                if absent_str in docs:
                    raise ValueError(
                        f"Sample #{i + 1} ({sample.get('input', 'unknown')}) absent string was found in documentation:\n"
                        f"  Forbidden string: {absent_str!r}"
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

        # Ensure all negative controls have a non-empty absent list
        negative_controls = [s for s in self.samples if "Not stated" in s.get("source", "")]
        self.assertEqual(len(negative_controls), 5, "Expected 5 negative control questions")
        for sample in negative_controls:
            self.assertIn("absent", sample, f"Negative control missing 'absent' field: {sample.get('input')}")
            self.assertIsInstance(sample["absent"], list)
            self.assertGreater(len(sample["absent"]), 0, f"Negative control has empty 'absent' list: {sample.get('input')}")

    def test_missing_id_field_fails(self):
        """Negative unit check: Samples missing the id field must fail validation."""
        bad_sample = [
            {
                "input": "Sample without id",
                "choices": ["A", "B", "C", "D"],
                "target": "A",
                "source": "README.md",
                "evidence": "CI for evals",
            }
        ]
        with self.assertRaises(ValueError) as ctx:
            validate_dataset(bad_sample, self.docs)
        self.assertIn("missing a valid 'id' field", str(ctx.exception))

    def test_empty_id_field_fails(self):
        """Negative unit check: Samples with an empty id string must fail validation."""
        bad_sample = [
            {
                "id": "   ",
                "input": "Sample with empty id",
                "choices": ["A", "B", "C", "D"],
                "target": "A",
                "source": "README.md",
                "evidence": "CI for evals",
            }
        ]
        with self.assertRaises(ValueError) as ctx:
            validate_dataset(bad_sample, self.docs)
        self.assertIn("missing a valid 'id' field", str(ctx.exception))

    def test_invalid_id_format_fails(self):
        """Negative unit check: Samples with uppercase, spaces, or non-kebab-case ids must fail validation."""
        for bad_id in ["Readme-Tagline", "readme_tagline", "readme tagline", "readme--tagline", "-readme", "readme-"]:
            bad_sample = [
                {
                    "id": bad_id,
                    "input": "Sample with bad id format",
                    "choices": ["A", "B", "C", "D"],
                    "target": "A",
                    "source": "README.md",
                    "evidence": "CI for evals",
                }
            ]
            with self.assertRaises(ValueError) as ctx:
                validate_dataset(bad_sample, self.docs)
            self.assertIn("invalid id format", str(ctx.exception))

    def test_duplicate_id_fails(self):
        """Negative unit check: Samples with duplicate ids must fail validation."""
        bad_samples = [
            {
                "id": "duplicate-id",
                "input": "Sample 1",
                "choices": ["A", "B", "C", "D"],
                "target": "A",
                "source": "README.md",
                "evidence": "CI for evals",
            },
            {
                "id": "duplicate-id",
                "input": "Sample 2",
                "choices": ["A", "B", "C", "D"],
                "target": "A",
                "source": "README.md",
                "evidence": "CI for evals",
            },
        ]
        with self.assertRaises(ValueError) as ctx:
            validate_dataset(bad_samples, self.docs)
        self.assertIn("duplicate id", str(ctx.exception))

    def test_load_dataset_has_explicit_sample_ids(self):
        """Verifies that docs_comprehension._load_dataset() maps sample IDs from the jsonl."""
        from docs_comprehension import _load_dataset

        ds = _load_dataset()
        self.assertEqual(len(ds), len(self.samples))
        for sample, raw in zip(ds, self.samples):
            self.assertEqual(sample.id, raw["id"])
            self.assertIsInstance(sample.id, str)
            self.assertTrue(bool(sample.id.strip()))

    def test_missing_evidence_field_fails(self):
        """Negative unit check: Samples missing the evidence field must fail validation."""
        bad_sample = [
            {
                "id": "test-sample",
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
                "id": "test-sample",
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
                "id": "test-sample",
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

    def test_absent_string_in_docs_fails(self):
        """Negative unit check: Samples with an absent string appearing in documentation must fail validation."""
        bad_sample = [
            {
                "id": "test-sample",
                "input": "Sample with forbidden string found in docs",
                "choices": ["A", "B", "C", "D"],
                "target": "A",
                "source": "README.md",
                "evidence": "CI for evals",
                "absent": ["eval-runner"],
            }
        ]
        with self.assertRaises(ValueError) as ctx:
            validate_dataset(bad_sample, self.docs)
        self.assertIn("absent string was found in documentation", str(ctx.exception))

    def test_invalid_absent_type_fails(self):
        """Negative unit check: Samples with an invalid absent field type or empty item must fail validation."""
        bad_sample_non_list = [
            {
                "id": "test-sample",
                "input": "Sample with non-list absent",
                "choices": ["A", "B", "C", "D"],
                "target": "A",
                "source": "README.md",
                "evidence": "CI for evals",
                "absent": "not_a_list",
            }
        ]
        with self.assertRaises(ValueError) as ctx:
            validate_dataset(bad_sample_non_list, self.docs)
        self.assertIn("'absent' field must be a list of strings", str(ctx.exception))

        bad_sample_empty_str = [
            {
                "id": "test-sample",
                "input": "Sample with empty absent item",
                "choices": ["A", "B", "C", "D"],
                "target": "A",
                "source": "README.md",
                "evidence": "CI for evals",
                "absent": ["   "],
            }
        ]
        with self.assertRaises(ValueError) as ctx:
            validate_dataset(bad_sample_empty_str, self.docs)
        self.assertIn("has an invalid absent string", str(ctx.exception))


if __name__ == "__main__":
    unittest.main()
