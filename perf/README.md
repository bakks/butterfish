# Butterfish Performance Harness

This repo includes two benchmark layers:

- Microbenchmarks for hot code paths in shell output processing.
- PTY integration benchmark for end-to-end shell wrapper throughput.

Use the local Go wrapper so module/cache writes stay inside the repo:

```bash
./bin/go-local test ./butterfish -run '^$' -bench . -benchmem
```

## Microbenchmarks

Run all microbenchmarks:

```bash
./bin/go-local test ./butterfish -run '^$' -bench 'Benchmark(ShellHistoryAppend4KB|ShellBufferWrite4KB|ParsePS1NoPrompt|ParsePS1WithPrompt|NewByteMsgCopy16KB|ShellOutputReplay)$' -benchmem
```

`BenchmarkShellOutputReplay` uses synthetic trace data by default. To replay real output:

```bash
BUTTERFISH_PERF_TRACE=/path/to/trace.bin ./bin/go-local test ./butterfish -run '^$' -bench BenchmarkShellOutputReplay -benchmem
```

## PTY Integration Benchmark

Build the CLI first:

```bash
./bin/go-local build -o ./bin/butterfish ./cmd/butterfish
```

Then run PTY benchmark:

```bash
BUTTERFISH_ENABLE_PTY_BENCH=1 ./bin/go-local test ./butterfish -run '^$' -bench BenchmarkPTYShellHighOutput -benchmem -benchtime=1x
```

Optional:

- `BUTTERFISH_PTY_BIN=/custom/path/butterfish` to benchmark a different binary.

## Benchstat Workflow

Capture baseline files:

```bash
make bench-micro-file OUT=/tmp/bf-micro-before.txt bench_count=5
make bench-pty-file OUT=/tmp/bf-pty-before.txt bench_count=5
```

Capture candidate files after your code change:

```bash
make bench-micro-file OUT=/tmp/bf-micro-after.txt bench_count=5
make bench-pty-file OUT=/tmp/bf-pty-after.txt bench_count=5
```

Compare:

```bash
make benchstat OLD=/tmp/bf-micro-before.txt NEW=/tmp/bf-micro-after.txt
make benchstat OLD=/tmp/bf-pty-before.txt NEW=/tmp/bf-pty-after.txt
```

Equivalent helper script:

```bash
./perf/benchstat.sh /tmp/bf-micro-before.txt /tmp/bf-micro-after.txt
```
