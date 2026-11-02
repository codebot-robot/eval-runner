# Agent Guidelines

## Code style

### Naming

- Spell identifiers out. Do not abbreviate a word to save characters: `candidateCommit` not `candCommit`, `requestedBy` not `reqBy`, `previousRun` not `prevRun`, `action` not `act`.
- An abbreviation is acceptable only when it pays for itself: it is universally understood by Go programmers and already used consistently in this codebase (`ctx`, `err`, `cfg`, `cr`, `req`/`resp` in HTTP handlers, `id`, `url`).
- Exception: loop variables may be single characters. `i`, `j`, `k` for integer loops are preferred; `for _, run := range runs` style is also fine.
- When extending existing code, match the existing name rather than introducing a second spelling; when adding new code, use the full word.
