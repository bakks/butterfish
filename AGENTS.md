# AGENTS

## Benchmarking Development

### Why this exists
- Butterfish shell performance is sensitive to terminal I/O behavior.
- Use the repo-local benchmark workflow so results are reproducible and do not depend on host Go cache/env setup.

### Local Go environment
- Always run benchmarks with `./bin/go-local` (or `make` targets that use it).
- This keeps `GOPATH`, `GOCACHE`, and `GOMODCACHE` under `.cache/go`.

### Key benchmark targets
- `make bench-butterfish`
  - Runs package benchmarks quickly for local iteration.
- `make bench-pty`
  - Runs end-to-end PTY benchmark once (`-benchtime=1x`).
- `make bench-micro-file OUT=/tmp/bf-micro-before.txt bench_count=5`
  - Captures microbench output for statistical comparison.
- `make bench-pty-file OUT=/tmp/bf-pty-before.txt bench_count=5`
  - Captures PTY benchmark output for statistical comparison.
- `make benchstat OLD=/tmp/bf-micro-before.txt NEW=/tmp/bf-micro-after.txt`
  - Compares benchmark files with `benchstat`.

### Recommended perf workflow
1. Capture baseline files (`before`).
2. Implement a focused change.
3. Capture candidate files (`after`) with identical flags/count.
4. Run `benchstat` for both micro and PTY files.
5. Confirm no functional regressions with `./bin/go-local test ./...`.

### PTY benchmark notes
- PTY benchmark is in `butterfish/perf_pty_bench_test.go`.
- It requires `BUTTERFISH_ENABLE_PTY_BENCH=1`.
- Marker detection waits for two marker hits (echoed input + command output) and enforces a minimum output byte threshold to avoid false-fast runs.

### TUI/Neovim performance notes
- TUI passthrough and bounded tail capture live in `butterfish/shell.go`.
- During interactive child sessions (e.g. `nvim`), Butterfish bypasses expensive output parsing/history accumulation and captures only a bounded sanitized tail for later history context.

### Logging and output hygiene
- Do not print raw PTY/binary streams directly to the user when debugging.
- Summarize benchmark outputs and key metrics instead of dumping noisy terminal byte content.
