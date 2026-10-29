# Development Tasks (`dev/tasks/`)

This directory contains automation and development task scripts.

> **Important**: `ap` automatically discovers executable scripts in `dev/tasks/` matching reserved prefixes:
> - `build-*`: Executed as build tasks during `ap build` and `ap deploy`.
> - `test-*`: Executed as test tasks during `ap test`.
> - `lint-*`: Executed as lint tasks during `ap lint`.
> - `test-e2e*`: Executed as end-to-end test tasks during `ap e2e`.
>
> New scripts added to `dev/tasks/` must pick prefixes deliberately. Standalone utility or maintenance scripts (such as `sandboxd-image` or `deploy-to-kube`) must use names outside these reserved prefixes so they are not run unexpectedly during every build or deploy cycle.
