package butterfish

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/creack/pty"
)

const (
	ptyBenchEnableEnv = "BUTTERFISH_ENABLE_PTY_BENCH"
	ptyBenchBinEnv    = "BUTTERFISH_PTY_BIN"
)

func resolvePTYBenchBinary() string {
	if fromEnv := os.Getenv(ptyBenchBinEnv); fromEnv != "" {
		return fromEnv
	}

	wd, err := os.Getwd()
	if err == nil {
		root := wd
		for {
			if _, statErr := os.Stat(filepath.Join(root, "go.mod")); statErr == nil {
				return filepath.Join(root, "bin", "butterfish")
			}
			next := filepath.Dir(root)
			if next == root {
				break
			}
			root = next
		}
	}

	return filepath.Join("..", "bin", "butterfish")
}

func waitForMarkerAndIdle(
	chunks <-chan []byte,
	errs <-chan error,
	marker string,
	// Number of marker observations required before considering command done.
	// In PTY mode we usually see the marker once in echoed input and once in
	// actual command output.
	requiredHits int,
	timeout time.Duration,
	idle time.Duration,
) (int, error) {
	markerBytes := []byte(marker)
	keepTail := len(markerBytes) - 1
	if keepTail < 0 {
		keepTail = 0
	}

	var tail []byte
	bytesRead := 0
	markerHits := 0
	markerSeen := false

	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()

	var idleTimer *time.Timer
	var idleC <-chan time.Time

	resetIdle := func() {
		if idleTimer == nil {
			idleTimer = time.NewTimer(idle)
			idleC = idleTimer.C
			return
		}

		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}
		idleTimer.Reset(idle)
	}

	for {
		select {
		case <-timeoutTimer.C:
			return bytesRead, fmt.Errorf("timed out waiting for marker %q", marker)

		case <-idleC:
			return bytesRead, nil

		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return bytesRead, err
			}

		case chunk, ok := <-chunks:
			if !ok {
				if markerSeen {
					return bytesRead, nil
				}
				return bytesRead, io.EOF
			}
			bytesRead += len(chunk)

			scan := append(tail, chunk...)
			if !markerSeen {
				hits := bytes.Count(scan, markerBytes)
				if hits > 0 {
					markerHits += hits
				}
				if markerHits >= requiredHits {
					markerSeen = true
					resetIdle()
				}
			} else if markerSeen {
				resetIdle()
			}

			if len(scan) > keepTail {
				tail = append([]byte(nil), scan[len(scan)-keepTail:]...)
			} else {
				tail = append([]byte(nil), scan...)
			}
		}
	}
}

func BenchmarkPTYShellHighOutput(b *testing.B) {
	if os.Getenv(ptyBenchEnableEnv) != "1" {
		b.Skipf("set %s=1 to enable PTY integration benchmark", ptyBenchEnableEnv)
	}

	binPath := resolvePTYBenchBinary()
	if _, err := os.Stat(binPath); err != nil {
		b.Fatalf("could not stat benchmark binary %q: %v", binPath, err)
	}

	cmd := exec.Command(binPath,
		"shell",
		"-A", // disable autosuggest and LLM calls in command path
		"-p", // keep prompt visuals simpler for benchmark output
		"-b", "/bin/zsh",
	)

	env := append([]string{}, os.Environ()...)
	env = append(env,
		"OPENAI_API_KEY=sk-butterfish-benchmark",
		"TERM=xterm-256color",
	)
	cmd.Env = env

	ws := &pty.Winsize{
		Rows: 40,
		Cols: 160,
	}

	ptmx, err := pty.StartWithSize(cmd, ws)
	if err != nil {
		b.Fatalf("failed to start PTY process: %v", err)
	}
	defer func() {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	chunks := make(chan []byte, 512)
	errs := make(chan error, 1)
	readDone := make(chan struct{})

	go func() {
		defer close(readDone)
		defer close(chunks)
		defer close(errs)

		buf := make([]byte, 32*1024)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				payload := make([]byte, n)
				copy(payload, buf[:n])
				chunks <- payload
			}
			if readErr != nil {
				if readErr != io.EOF {
					errs <- readErr
				}
				return
			}
		}
	}()

	readyMarker := "__BF_READY__"
	if _, err := ptmx.Write([]byte("echo " + readyMarker + "\r")); err != nil {
		b.Fatalf("failed writing readiness command: %v", err)
	}
	if _, err := waitForMarkerAndIdle(chunks, errs, readyMarker, 2, 15*time.Second, 250*time.Millisecond); err != nil {
		b.Fatalf("failed waiting for readiness marker: %v", err)
	}

	const lineCount = 200000
	const expectedBytesApprox = 7 * lineCount
	b.SetBytes(expectedBytesApprox)
	b.ReportAllocs()

	var totalBytes int64
	var totalDuration time.Duration

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		marker := fmt.Sprintf("__BF_DONE_%d__", i)
		command := fmt.Sprintf("seq 1 %d; echo %s\r", lineCount, marker)

		start := time.Now()
		if _, err := ptmx.Write([]byte(command)); err != nil {
			b.Fatalf("failed writing benchmark command: %v", err)
		}

		bytesRead, err := waitForMarkerAndIdle(chunks, errs, marker, 2, 30*time.Second, 250*time.Millisecond)
		if err != nil {
			b.Fatalf("failed waiting for completion marker: %v", err)
		}
		// Guard against short-circuit runs where we only matched echoed input.
		if bytesRead < expectedBytesApprox/2 {
			b.Fatalf("unexpectedly small PTY output: got %d bytes, expected at least %d", bytesRead, expectedBytesApprox/2)
		}

		elapsed := time.Since(start)
		totalDuration += elapsed
		totalBytes += int64(bytesRead)
	}
	b.StopTimer()

	if b.N > 0 {
		b.ReportMetric(float64(totalBytes)/float64(b.N), "bytes/op")
		b.ReportMetric(float64(totalDuration.Milliseconds())/float64(b.N), "ms/op")
		if totalDuration > 0 {
			b.ReportMetric(float64(totalBytes)/(1024.0*1024.0)/totalDuration.Seconds(), "MB/s")
		}
	}

	benchmarkSinkInt = int(totalBytes)
}
