<!--
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Agent Guidelines

## Code style

### Naming

- Spell identifiers out. Do not abbreviate a word to save characters: `candidateCommit` not `candCommit`, `requestedBy` not `reqBy`, `previousRun` not `prevRun`, `action` not `act`.
- An abbreviation is acceptable only when it pays for itself: it is universally understood by Go programmers and already used consistently in this codebase (`ctx`, `err`, `cfg`, `cr`, `req`/`resp` in HTTP handlers, `id`, `url`).
- Exception: loop variables may be single characters. `i`, `j`, `k` for integer loops are preferred; `for _, run := range runs` style is also fine.
- When extending existing code, match the existing name rather than introducing a second spelling; when adding new code, use the full word.
