# UI Concept and Mockups

This directory contains concept mockups and UI route notes for `eval-runner`.

For the primary endpoint list and architecture documentation, see [docs/architecture.md](../../architecture.md).

## Embeddable Badge and Trend Query Parameters

The `badge.svg`, `sparkline.svg`, and `badge.json` endpoints support query filters:

- `?branch=`: Branch name (defaults to `main`).
- `?eval=<task-id>`: Narrows score calculation to a single task ID. The badge left label displays the task ID (truncated to 40 characters if longer).
- `?cohort=`: Optional cohort filter.
- `?w=` and `?h=` (`sparkline.svg` only): Dimensions in pixels (default `120x28`, clamped to width `[30, 1000]` and height `[10, 300]`).

### Badge Color Thresholds
- **Green**: Score ≥ 80%
- **Yellow**: 50% ≤ Score < 80%
- **Red**: Score < 50%
- **Grey**: `no data` (no completed observations recorded)

*Note on Headline Scores:* The badge endpoint displays the pooled Wilson estimate (`OverallEst.Point`) across all tasks. The repository overview page displays `OverallScore`, the unweighted mean of suite scores. When suites have unequal numbers of tasks or samples, these values may differ slightly.

## Mockups

Some auto-generated mockups that show what the UI could look like.
