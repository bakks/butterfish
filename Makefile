
gofiles := $(shell find . -name '*.go' -type f -not -path "./vendor/*")
buildtime := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
commit := $(shell bash ./bin/gitcommit.sh)
flags := -X main.BuildVersion=dev -X main.BuildCommit=${commit} -X main.BuildTimestamp=${buildtime}
# Use repo-local Go env/cache paths to avoid host Go env issues.
go_local := go
# Number of benchmark samples to collect in file capture targets.
bench_count ?= 5

all: build test

bin/butterfish: $(gofiles) Makefile go.mod go.sum
	mkdir -p bin
	$(go_local) build -ldflags "${flags}" -o ./bin/butterfish ./cmd/butterfish

clean:
	rm -f bin/butterfish proto/ibodai*.go

watch: Makefile
	find . -name "*.go" -o -name "*.proto" | entr -c make

test:
	$(go_local) test ./...

test-local:
	$(go_local) test ./...

bench: bench-butterfish

# Run all benchmarks in the butterfish package (quick local iteration).
bench-butterfish:
	$(go_local) test ./butterfish -run '^$$' -bench . -benchmem

# Run end-to-end PTY benchmark once (slower, closer to real shell usage).
bench-pty: bin/butterfish
	BUTTERFISH_ENABLE_PTY_BENCH=1 $(go_local) test ./butterfish -run '^$$' -bench BenchmarkPTYShellHighOutput -benchmem -benchtime=1x

# Capture microbenchmark output to a file for benchstat comparisons.
bench-micro-file:
	@test -n "$(OUT)" || (echo "Usage: make bench-micro-file OUT=/tmp/bench.txt [bench_count=5]" && exit 1)
	$(go_local) test ./butterfish -run '^$$' -bench 'Benchmark(ShellHistoryAppend4KB|ShellBufferWrite4KB|ShellOutputReplay)$$' -benchmem -count=$(bench_count) > $(OUT)

# Capture PTY benchmark output to a file for benchstat comparisons.
bench-pty-file: bin/butterfish
	@test -n "$(OUT)" || (echo "Usage: make bench-pty-file OUT=/tmp/bench.txt [bench_count=5]" && exit 1)
	BUTTERFISH_ENABLE_PTY_BENCH=1 $(go_local) test ./butterfish -run '^$$' -bench BenchmarkPTYShellHighOutput -benchmem -benchtime=1x -count=$(bench_count) > $(OUT)

# Compare two benchmark files (OLD vs NEW) with benchstat.
benchstat:
	@test -n "$(OLD)" || (echo "Usage: make benchstat OLD=/tmp/before.txt NEW=/tmp/after.txt" && exit 1)
	@test -n "$(NEW)" || (echo "Usage: make benchstat OLD=/tmp/before.txt NEW=/tmp/after.txt" && exit 1)
	$(go_local) run golang.org/x/perf/cmd/benchstat@latest $(OLD) $(NEW)

build: bin/butterfish

install: bin/butterfish
	cp bin/butterfish /usr/local/bin

licenses:
	go-licenses report ./... 2>/dev/null | awk -F"," '{printf "|[%s](https://%s)|[%s](%s)|\n",$$1,$$1,$$3,$$2}'


.PHONY: all clean watch test test-local bench bench-butterfish bench-pty bench-micro-file bench-pty-file benchstat build licenses install
