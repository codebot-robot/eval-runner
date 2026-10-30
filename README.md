# eval-runner

[![.evals](https://dev.dotevals.com/github.com/gke-labs/eval-runner/badge.svg)](https://dev.dotevals.com/github.com/gke-labs/eval-runner) ![trend](https://dev.dotevals.com/github.com/gke-labs/eval-runner/sparkline.svg)

CI for evals: efficient statistical estimation of agentic goodness.  
*CI stands for confidence interval.*

`eval-runner` is a Kubernetes-native continuous integration system that discovers the evals in a repository, executes them in isolated sandboxes on every pull request, and uses statistical estimation to determine whether agent changes represent a measurable improvement or regression.

> **Status: early development.** Today the controller performs eval discovery (runs `.evals/list` in an agent-sandbox), documented in [docs/architecture.md](docs/architecture.md); sample execution, statistical verdicts and PR integration are described in [docs/concept/functionality.md](docs/concept/functionality.md) and are not yet implemented.

## Core Concepts

- **An agent is a distribution, not a function:** The same agent input yields variable outputs across runs. The unit of measurement is therefore a success rate with a confidence interval, rather than a binary pass/fail.
- **Eval discovery via code:** Evals are discovered by executing an entrypoint (`.evals/list`) at the repository root. This executable outputs a JSON manifest detailing tasks, working directories, and declarative resource requirements.
- **Isolated sandboxes:** Each sample runs in an isolated pod (e.g. using `kubernetes-sigs/agent-sandbox`) with only the declared resources (such as Docker or Kind).
- **Statistical verdicts:** By running paired samples between the PR head commit and the base commit, `eval-runner` produces a run-level verdict: `IMPROVED`, `REGRESSED`, `UNCHANGED`, or `INCONCLUSIVE`.
- **Framework compatibility:** Built-in native support for UK AISI Inspect (`inspect_ai`), with format-agnostic extensibility for any evaluation framework.

## Documentation

- [System Architecture](docs/architecture.md): Overview of the system as implemented on `main` today, components, trust model, and implementation status.
- [Architecture Decisions](docs/decisions.md): Architectural decision records (ADR log) documenting design choices and rationales.
- [Development Guide](docs/development.md): Prerequisites, local development workflows, running unit and E2E tests, and running local Inspect evals.
- [Functional Description](docs/concept/functionality.md): Detailed architecture, execution lifecycle, statistical gates, and UI mockups.
- [Contributing](docs/contributing.md): Contribution guidelines and community standards.

## Recommended repository setup

1. **Required Status Check**: Add `eval-runner / evals` as a required status check in a repository ruleset or branch protection rule once evaluation verdicts are trusted, ensuring regressions cannot merge into tracked branches.
2. **About Website**: Point your repository's GitHub "About" website URL at the eval-runner repo page (e.g. `https://dotevals.com/github.com/<owner>/<repo>`) so visitors can immediately see current evaluation scores and historical series trends without having to open the README.

## Contributing

This project is licensed under the [Apache 2.0 License](LICENSE).

We welcome contributions! Please see [docs/contributing.md](docs/contributing.md) for more information.

We follow [Google's Open Source Community Guidelines](https://opensource.google.com/conduct/).

## Disclaimer

This is not an officially supported Google product.

This project is not eligible for the Google Open Source Software Vulnerability Rewards Program.
