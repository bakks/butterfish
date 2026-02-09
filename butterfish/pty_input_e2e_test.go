package butterfish

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// PTY E2E coverage for shell/prompt editing behavior.
//
// These tests drive a real butterfish shell process over a pseudo-terminal,
// send key sequences and paste-like bursts, then assert semantic outcomes
// against sanitized output (ANSI/control bytes removed).
//
// Notes:
// - We emulate cursor-position responses (ESC[6n -> ESC[row;colR]) because
//   prompt mode relies on querying the terminal for column offsets.
// - For prompt-local commands (e.g. "Help"), assertions avoid network/LLM
//   dependencies and focus on editing/state transitions.
// - Marker-based capture ("echo __BF_E2E_DONE__") lets each test bound
//   output deterministically without printing raw PTY streams.

var (
	e2eBuildOnce sync.Once
	e2eBinary    string
	e2eBuildErr  error
)

func repoRootForTests(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}

	root := wd
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return root
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatalf("could not find repo root from %q", wd)
		}
		root = parent
	}
}

func ensureE2EBinary(t *testing.T) string {
	t.Helper()

	e2eBuildOnce.Do(func() {
		root := repoRootForTests(t)
		outPath := filepath.Join(os.TempDir(), "butterfish-e2e-bin")
		cmd := exec.Command("go", "build", "-o", outPath, "./cmd/butterfish")
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			e2eBuildErr = fmt.Errorf("failed to build e2e binary: %w\n%s", err, string(out))
			return
		}
		e2eBinary = outPath
	})

	if e2eBuildErr != nil {
		t.Fatalf("%v", e2eBuildErr)
	}
	return e2eBinary
}

type shellE2EHarness struct {
	t        *testing.T
	cmd      *exec.Cmd
	ptmx     *os.File
	rawState *term.State
	zdot     string
	chunks   chan []byte
	errs     chan error
}

const (
	caseIdleWindow  = 250 * time.Millisecond
	caseSettleDelay = 400 * time.Millisecond
)

func startShellE2E(t *testing.T) *shellE2EHarness {
	return startShellE2EWithConfig(t, 40, 160, 1, 1)
}

// startShellE2EWithConfig boots butterfish in an isolated zsh PTY.
// rows/cols control wrapping behavior; cursorRow/cursorCol define the
// synthetic cursor-position response for ESC[6n requests.
func startShellE2EWithConfig(
	t *testing.T,
	rows uint16,
	cols uint16,
	cursorRow int,
	cursorCol int,
) *shellE2EHarness {
	t.Helper()

	bin := ensureE2EBinary(t)
	zdotdir, mkErr := os.MkdirTemp("", "butterfish-e2e-zdot-*")
	if mkErr != nil {
		t.Fatalf("failed creating temp ZDOTDIR: %v", mkErr)
	}

	cmd := exec.Command(bin,
		"shell",
		"-A",
		"-b", "/bin/zsh",
	)
	cmd.Env = append([]string{}, os.Environ()...)
	cmd.Env = append(cmd.Env,
		"OPENAI_API_KEY=sk-butterfish-e2e",
		"TERM=xterm-256color",
		"ZDOTDIR="+zdotdir,
		"HISTFILE=/dev/null",
		"SAVEHIST=0",
	)

	ws := &pty.Winsize{Rows: rows, Cols: cols}
	ptmx, err := pty.StartWithSize(cmd, ws)
	if err != nil {
		t.Fatalf("failed to start shell e2e harness: %v", err)
	}
	rawState, err := term.MakeRaw(int(ptmx.Fd()))
	if err != nil {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		t.Fatalf("failed setting harness PTY to raw mode: %v", err)
	}

	h := &shellE2EHarness{
		t:        t,
		cmd:      cmd,
		ptmx:     ptmx,
		rawState: rawState,
		zdot:     zdotdir,
		chunks:   make(chan []byte, 512),
		errs:     make(chan error, 1),
	}

	go func() {
		defer close(h.chunks)
		defer close(h.errs)

		cursorQuery := []byte(ESC_CUP)
		cursorResp := []byte(fmt.Sprintf("\x1b[%d;%dR", cursorRow, cursorCol))
		buf := make([]byte, 32*1024)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				payload := make([]byte, n)
				copy(payload, buf[:n])
				queryCount := bytes.Count(payload, cursorQuery)
				for i := 0; i < queryCount; i++ {
					_, _ = ptmx.Write(cursorResp)
				}
				h.chunks <- payload
			}
			if readErr != nil {
				if readErr != io.EOF {
					h.errs <- readErr
				}
				return
			}
		}
	}()

	readyMarker := "__BF_E2E_READY__"
	if _, err = h.writeStepsWithIdle(bytesToKeySteps("echo "+readyMarker), 2*time.Second, 40*time.Millisecond); err != nil {
		h.close()
		t.Fatalf("failed typing readiness marker: %v", err)
	}
	if err = h.writeAll([]byte("\r")); err != nil {
		h.close()
		t.Fatalf("failed submitting readiness marker command: %v", err)
	}
	_, err = waitForMarkerAndIdleCapture(h.chunks, h.errs, readyMarker, 1, 12*time.Second, caseIdleWindow)
	if err != nil {
		h.close()
		t.Fatalf("failed waiting for shell readiness: %v", err)
	}
	_, _ = waitForIdleCapture(h.chunks, h.errs, 2*time.Second, 100*time.Millisecond)

	return h
}

func (h *shellE2EHarness) writeAll(payload []byte) error {
	for len(payload) > 0 {
		n, err := h.ptmx.Write(payload)
		if err != nil {
			return fmt.Errorf("write failed: %w", err)
		}
		if n <= 0 {
			return fmt.Errorf("write returned %d bytes", n)
		}
		payload = payload[n:]
	}
	return nil
}

// bytesToKeySteps splits text to byte-at-a-time keypress steps.
// This exercises state transitions similarly to interactive typing.
func bytesToKeySteps(text string) [][]byte {
	steps := make([][]byte, 0, len(text))
	for i := 0; i < len(text); i++ {
		steps = append(steps, []byte{text[i]})
	}
	return steps
}

// writeStepsWithIdle writes each step and captures output until short idle.
// This helps keep test actions and observed output synchronized.
func (h *shellE2EHarness) writeStepsWithIdle(
	steps [][]byte,
	timeout time.Duration,
	idle time.Duration,
) (string, error) {
	var out strings.Builder
	for i, step := range steps {
		if err := h.writeAll(step); err != nil {
			return out.String(), fmt.Errorf("write step %d failed: %w", i, err)
		}
		chunk, err := waitForIdleCapture(h.chunks, h.errs, timeout, idle)
		out.WriteString(chunk)
		if err != nil && !errors.Is(err, io.EOF) {
			return out.String(), fmt.Errorf("capture step %d failed: %w", i, err)
		}
	}
	return out.String(), nil
}

func (h *shellE2EHarness) close() {
	if h.rawState != nil {
		_ = term.Restore(int(h.ptmx.Fd()), h.rawState)
	}
	_ = h.ptmx.Close()
	if h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
	}
	_ = h.cmd.Wait()
	if h.zdot != "" {
		_ = os.RemoveAll(h.zdot)
	}
}

// waitForMarkerAndIdleCapture reads output until marker is observed, then
// waits for a short idle period before returning accumulated output.
func waitForMarkerAndIdleCapture(
	chunks <-chan []byte,
	errs <-chan error,
	marker string,
	requiredHits int,
	timeout time.Duration,
	idle time.Duration,
) (string, error) {
	markerBytes := []byte(marker)
	keepTail := len(markerBytes) - 1
	if keepTail < 0 {
		keepTail = 0
	}

	var output bytes.Buffer
	var tail []byte
	hits := 0
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
			return output.String(), fmt.Errorf("timed out waiting for marker %q", marker)
		case <-idleC:
			return output.String(), nil
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return output.String(), err
			}
		case chunk, ok := <-chunks:
			if !ok {
				if markerSeen {
					return output.String(), nil
				}
				return output.String(), io.EOF
			}
			output.Write(chunk)

			scan := append(tail, chunk...)
			if !markerSeen {
				n := bytes.Count(scan, markerBytes)
				if n > 0 {
					hits += n
				}
				if hits >= requiredHits {
					markerSeen = true
					resetIdle()
				}
			} else {
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

// waitForIdleCapture returns once output has been quiet for `idle`.
func waitForIdleCapture(
	chunks <-chan []byte,
	errs <-chan error,
	timeout time.Duration,
	idle time.Duration,
) (string, error) {
	var output bytes.Buffer

	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()

	idleTimer := time.NewTimer(idle)
	defer idleTimer.Stop()

	for {
		select {
		case <-timeoutTimer.C:
			return output.String(), fmt.Errorf("timed out waiting for idle output")
		case <-idleTimer.C:
			return output.String(), nil
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return output.String(), err
			}
		case chunk, ok := <-chunks:
			if !ok {
				return output.String(), io.EOF
			}
			output.Write(chunk)
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(idle)
		}
	}
}

// runCase is the simple single-payload form of runCaseSteps.
func (h *shellE2EHarness) runCase(
	payload []byte,
	doneMarker string,
	requiredHits int,
	timeout time.Duration,
) (string, error) {
	return h.runCaseSteps([][]byte{payload}, doneMarker, requiredHits, timeout)
}

// runCaseSteps executes scenario steps, then emits/awaits a done marker so
// each test captures a bounded output window.
func (h *shellE2EHarness) runCaseSteps(
	steps [][]byte,
	doneMarker string,
	requiredHits int,
	timeout time.Duration,
) (string, error) {
	_, _ = waitForIdleCapture(h.chunks, h.errs, 2*time.Second, 100*time.Millisecond)
	time.Sleep(caseSettleDelay)

	prelude, err := h.writeStepsWithIdle(steps, 2*time.Second, 40*time.Millisecond)
	if err != nil {
		return prelude, err
	}

	markerPrelude, err := h.writeStepsWithIdle(bytesToKeySteps("echo "+doneMarker), 2*time.Second, 40*time.Millisecond)
	if err != nil {
		return prelude + markerPrelude, fmt.Errorf("typing done marker failed: %w", err)
	}
	if err := h.writeAll([]byte("\r")); err != nil {
		return prelude + markerPrelude, fmt.Errorf("submitting done marker failed: %w", err)
	}
	postlude, err := waitForMarkerAndIdleCapture(h.chunks, h.errs, doneMarker, requiredHits, timeout, caseIdleWindow)
	return prelude + markerPrelude + postlude, err
}

// splitToChunks breaks a large paste payload into realistic write chunks.
func splitToChunks(s string, chunkSize int) []string {
	if chunkSize <= 0 {
		return []string{s}
	}
	chunks := make([]string, 0, (len(s)+chunkSize-1)/chunkSize)
	for i := 0; i < len(s); i += chunkSize {
		end := i + chunkSize
		if end > len(s) {
			end = len(s)
		}
		chunks = append(chunks, s[i:end])
	}
	return chunks
}

func appendStepRepeat(steps [][]byte, payload []byte, count int) [][]byte {
	for i := 0; i < count; i++ {
		steps = append(steps, payload)
	}
	return steps
}

// bracketedPaste wraps text in terminal bracketed-paste delimiters.
func bracketedPaste(text string) []byte {
	return []byte("\x1b[200~" + text + "\x1b[201~")
}

// Basic prompt-local burst path: uppercase starts prompt mode and should still
// resolve local Help command on submit.
func TestPTYShellPromptPasteBurstHelp(t *testing.T) {
	h := startShellE2E(t)
	defer h.close()

	out, err := h.runCase([]byte("Help\r"), "__BF_E2E_DONE_HELP__", 1, 15*time.Second)
	if err != nil {
		t.Fatalf("runCase failed: %v", err)
	}
	clean := sanitizeTTYString(out)

	if !strings.Contains(clean, "You're using the Butterfish Shell Mode") {
		t.Fatalf("help output missing, got: %.400q", clean)
	}
}

// Shell command editing: move left twice and insert in the middle.
func TestPTYShellCursorLeftInsert(t *testing.T) {
	h := startShellE2E(t)
	defer h.close()

	payload := []byte("echo abcd\x1b[D\x1b[DX\r")
	out, err := h.runCase(payload, "__BF_E2E_DONE_LEFT__", 1, 15*time.Second)
	if err != nil {
		t.Fatalf("runCase failed: %v", err)
	}
	clean := sanitizeTTYString(out)

	if !strings.Contains(clean, "abXcd") {
		t.Fatalf("expected edited shell output to contain abXcd, got: %.400q", clean)
	}
}

// Prompt editing with left/backspace around a local Help command.
func TestPTYShellPromptCursorMoveAndBackspace(t *testing.T) {
	h := startShellE2E(t)
	defer h.close()

	out, err := h.runCaseSteps([][]byte{
		[]byte("H"),
		[]byte("e"),
		[]byte("l"),
		[]byte("p"),
		[]byte("\x1b[D"),
		[]byte("\x1b[D"),
		[]byte("X"),
		[]byte("\x7f"),
		[]byte("\r"),
	}, "__BF_E2E_DONE_PROMPT1__", 1, 15*time.Second)
	if err != nil {
		t.Fatalf("runCase failed: %v", err)
	}
	clean := sanitizeTTYString(out)

	if !strings.Contains(clean, "You're using the Butterfish Shell Mode") {
		t.Fatalf("prompt editing did not resolve to Help, got: %.400q", clean)
	}
}

// Prompt editing with ctrl-a/ctrl-e cursor movement.
func TestPTYShellPromptCtrlAAndCtrlE(t *testing.T) {
	h := startShellE2E(t)
	defer h.close()

	out, err := h.runCaseSteps([][]byte{
		[]byte("H"),
		[]byte("l"),
		[]byte("p"),
		[]byte{0x01},
		[]byte("\x1b[C"),
		[]byte("e"),
		[]byte{0x05},
		[]byte("\r"),
	}, "__BF_E2E_DONE_PROMPT2__", 1, 15*time.Second)
	if err != nil {
		t.Fatalf("runCase failed: %v", err)
	}
	clean := sanitizeTTYString(out)

	if !strings.Contains(clean, "You're using the Butterfish Shell Mode") {
		t.Fatalf("ctrl-a/ctrl-e prompt editing did not resolve to Help, got: %.400q", clean)
	}
}

// Prompt editing with alt-word left/right sequences.
func TestPTYShellAltWordJump(t *testing.T) {
	h := startShellE2E(t)
	defer h.close()

	out, err := h.runCaseSteps([][]byte{
		[]byte("H"),
		[]byte("e"),
		[]byte("l"),
		[]byte("p"),
		[]byte("\x1b[1;3D"),
		[]byte("\x1b[1;3C"),
		[]byte("\r"),
	}, "__BF_E2E_DONE_ALT__", 1, 15*time.Second)
	if err != nil {
		t.Fatalf("runCase failed: %v", err)
	}
	clean := sanitizeTTYString(out)

	if !strings.Contains(clean, "You're using the Butterfish Shell Mode") {
		t.Fatalf("expected alt-word jump to preserve Help command, got: %.400q", clean)
	}
}

// Large paste into a shell command line, chunked to mimic terminal behavior.
func TestPTYShellLargePasteBurst(t *testing.T) {
	h := startShellE2E(t)
	defer h.close()

	pasted := strings.Repeat("abc123", 600)
	steps := [][]byte{
		[]byte("e"),
		[]byte("c"),
		[]byte("h"),
		[]byte("o"),
		[]byte(" "),
		[]byte("\r"),
	}
	pasteChunks := splitToChunks(pasted, 256)
	steps = steps[:5]
	for _, chunk := range pasteChunks {
		steps = append(steps, []byte(chunk))
	}
	steps = append(steps, []byte("\r"))

	out, err := h.runCaseSteps(steps, "__BF_E2E_DONE_PASTE__", 1, 45*time.Second)
	if err != nil {
		t.Fatalf("runCase failed: %v; output: %.400q", err, sanitizeTTYString(out))
	}
	clean := sanitizeTTYString(out)

	prefix := pasted[:96]
	suffix := pasted[len(pasted)-96:]
	if !strings.Contains(clean, prefix) {
		t.Fatalf("large paste prefix missing from output, got: %.400q", clean)
	}
	if !strings.Contains(clean, suffix) {
		t.Fatalf("large paste suffix missing from output, got: %.400q", clean)
	}
}

// Single burst command with carriage return in same read chunk.
func TestPTYShellBurstCommandWithCR(t *testing.T) {
	h := startShellE2E(t)
	defer h.close()

	out, err := h.runCase([]byte("echo burst_ok\r"), "__BF_E2E_DONE_BURST__", 1, 15*time.Second)
	if err != nil {
		t.Fatalf("runCase failed: %v", err)
	}
	clean := sanitizeTTYString(out)

	if !strings.Contains(clean, "burst_ok") {
		t.Fatalf("burst command output missing, got: %.400q", clean)
	}
}

// Prompt ctrl-c should cancel prompt mode and allow normal shell command next.
func TestPTYShellPromptCtrlCCancelThenShellCommand(t *testing.T) {
	h := startShellE2E(t)
	defer h.close()

	out, err := h.runCaseSteps([][]byte{
		[]byte("H"),
		[]byte("e"),
		[]byte("l"),
		[]byte("p"),
		[]byte{0x03},
		[]byte("e"),
		[]byte("c"),
		[]byte("h"),
		[]byte("o"),
		[]byte(" "),
		[]byte("a"),
		[]byte("f"),
		[]byte("t"),
		[]byte("e"),
		[]byte("r"),
		[]byte("_"),
		[]byte("c"),
		[]byte("t"),
		[]byte("r"),
		[]byte("l"),
		[]byte("c"),
		[]byte("\r"),
	}, "__BF_E2E_DONE_PROMPT_CTRLC__", 1, 15*time.Second)
	if err != nil {
		t.Fatalf("runCase failed: %v", err)
	}
	clean := sanitizeTTYString(out)

	if !strings.Contains(clean, "after_ctrlc") {
		t.Fatalf("post-ctrl-c shell command output missing, got: %.400q", clean)
	}
}

// Shell ctrl-c should cancel in-progress command and allow next command.
func TestPTYShellCommandCtrlCCancelThenRun(t *testing.T) {
	h := startShellE2E(t)
	defer h.close()

	out, err := h.runCaseSteps([][]byte{
		[]byte("e"),
		[]byte("c"),
		[]byte("h"),
		[]byte("o"),
		[]byte(" "),
		[]byte("n"),
		[]byte("o"),
		[]byte("_"),
		[]byte("r"),
		[]byte("u"),
		[]byte("n"),
		[]byte{0x03},
		[]byte("e"),
		[]byte("c"),
		[]byte("h"),
		[]byte("o"),
		[]byte(" "),
		[]byte("y"),
		[]byte("e"),
		[]byte("s"),
		[]byte("_"),
		[]byte("r"),
		[]byte("u"),
		[]byte("n"),
		[]byte("\r"),
	}, "__BF_E2E_DONE_SHELL_CTRLC__", 1, 15*time.Second)
	if err != nil {
		t.Fatalf("runCase failed: %v", err)
	}
	clean := sanitizeTTYString(out)

	if !strings.Contains(clean, "yes_run") {
		t.Fatalf("expected yes_run output after command ctrl-c, got: %.400q", clean)
	}
}

// Up-arrow passthrough should not poison subsequent command handling.
func TestPTYShellUpArrowThenCommand(t *testing.T) {
	h := startShellE2E(t)
	defer h.close()

	out, err := h.runCaseSteps([][]byte{
		[]byte("\x1b[A"),
		[]byte("e"),
		[]byte("c"),
		[]byte("h"),
		[]byte("o"),
		[]byte(" "),
		[]byte("u"),
		[]byte("p"),
		[]byte("_"),
		[]byte("o"),
		[]byte("k"),
		[]byte("\r"),
	}, "__BF_E2E_DONE_UP__", 1, 15*time.Second)
	if err != nil {
		t.Fatalf("runCase failed: %v", err)
	}
	clean := sanitizeTTYString(out)

	if !strings.Contains(clean, "up_ok") {
		t.Fatalf("up-arrow + command path missing expected output, got: %.400q", clean)
	}
}

// Mid-command paste burst should preserve both prefix and suffix edits.
func TestPTYShellMidCommandPasteBurst(t *testing.T) {
	h := startShellE2E(t)
	defer h.close()

	pasted := strings.Repeat("xyz", 128)
	out, err := h.runCaseSteps([][]byte{
		[]byte("e"),
		[]byte("c"),
		[]byte("h"),
		[]byte("o"),
		[]byte(" "),
		[]byte("P"),
		[]byte("R"),
		[]byte("E"),
		[]byte("_"),
		[]byte(pasted),
		[]byte("_"),
		[]byte("P"),
		[]byte("O"),
		[]byte("S"),
		[]byte("T"),
		[]byte("\r"),
	}, "__BF_E2E_DONE_MIDPASTE__", 1, 20*time.Second)
	if err != nil {
		t.Fatalf("runCase failed: %v; output: %.400q", err, sanitizeTTYString(out))
	}
	clean := sanitizeTTYString(out)

	if !strings.Contains(clean, "PRE_") {
		t.Fatalf("mid-command paste prefix missing, got: %.400q", clean)
	}
	if !strings.Contains(clean, "_POST") {
		t.Fatalf("mid-command paste suffix missing, got: %.400q", clean)
	}
}

// Wrapped prompt line: cursor-back + paste + delete should recover Help.
func TestPTYShellPromptWrapCursorBackPaste(t *testing.T) {
	h := startShellE2EWithConfig(t, 24, 24, 1, 22)
	defer h.close()

	steps := bytesToKeySteps("Help")
	steps = appendStepRepeat(steps, []byte("\x1b[D"), 3)
	steps = append(steps, []byte(strings.Repeat("z", 24)))
	steps = appendStepRepeat(steps, []byte("\x7f"), 24)
	steps = append(steps, []byte("\r"))

	out, err := h.runCaseSteps(steps, "__BF_E2E_DONE_WRAP_PROMPT__", 1, 20*time.Second)
	if err != nil {
		t.Fatalf("runCase failed: %v", err)
	}
	clean := sanitizeTTYString(out)
	if !strings.Contains(clean, "You're using the Butterfish Shell Mode") {
		t.Fatalf("wrapped prompt edit/paste did not resolve to Help")
	}
}

// Wrapped prompt line: paste plus arrow motion then delete.
func TestPTYShellPromptWrapPasteAndArrowEdit(t *testing.T) {
	h := startShellE2EWithConfig(t, 24, 24, 1, 23)
	defer h.close()

	insert := strings.Repeat("xy", 16)
	steps := bytesToKeySteps("Help")
	steps = appendStepRepeat(steps, []byte("\x1b[D"), 2)
	steps = append(steps, []byte(insert))
	steps = appendStepRepeat(steps, []byte("\x1b[C"), 2)
	steps = appendStepRepeat(steps, []byte("\x1b[D"), 2)
	steps = appendStepRepeat(steps, []byte("\x7f"), len(insert))
	steps = append(steps, []byte("\r"))

	out, err := h.runCaseSteps(steps, "__BF_E2E_DONE_WRAP_PROMPT2__", 1, 20*time.Second)
	if err != nil {
		t.Fatalf("runCase failed: %v", err)
	}
	clean := sanitizeTTYString(out)
	if !strings.Contains(clean, "You're using the Butterfish Shell Mode") {
		t.Fatalf("wrapped prompt paste+arrow edit did not resolve to Help")
	}
}

// Wrapped prompt line variant using ctrl-e before reverse edits/deletes.
func TestPTYShellPromptWrapPasteCtrlEEdit(t *testing.T) {
	h := startShellE2EWithConfig(t, 24, 24, 1, 23)
	defer h.close()

	insert := strings.Repeat("k", 20)
	steps := bytesToKeySteps("Help")
	steps = appendStepRepeat(steps, []byte("\x1b[D"), 2)
	steps = append(steps, []byte(insert))
	steps = append(steps, []byte{0x05}) // ctrl-e
	steps = appendStepRepeat(steps, []byte("\x1b[D"), 2)
	steps = appendStepRepeat(steps, []byte("\x7f"), len(insert))
	steps = append(steps, []byte("\r"))

	out, err := h.runCaseSteps(steps, "__BF_E2E_DONE_WRAP_PROMPT3__", 1, 20*time.Second)
	if err != nil {
		t.Fatalf("runCase failed: %v", err)
	}
	clean := sanitizeTTYString(out)
	if !strings.Contains(clean, "You're using the Butterfish Shell Mode") {
		t.Fatalf("wrapped prompt paste+ctrl-e edit did not resolve to Help")
	}
}

// Wrapped shell command line: move back and paste in the middle.
func TestPTYShellCommandWrapCursorBackPaste(t *testing.T) {
	h := startShellE2EWithConfig(t, 24, 24, 1, 22)
	defer h.close()

	base := strings.Repeat("ab", 28)
	insert := strings.Repeat("Q", 18)
	expected := base[:len(base)-12] + insert + base[len(base)-12:]

	steps := bytesToKeySteps("echo " + base)
	steps = appendStepRepeat(steps, []byte("\x1b[D"), 12)
	steps = append(steps, []byte(insert))
	steps = append(steps, []byte("\r"))

	out, err := h.runCaseSteps(steps, "__BF_E2E_DONE_WRAP_SHELL__", 1, 20*time.Second)
	if err != nil {
		t.Fatalf("runCase failed: %v", err)
	}
	clean := sanitizeTTYString(out)
	if !strings.Contains(clean, expected) {
		t.Fatalf("wrapped shell edit/paste output mismatch")
	}
}

// Sweep cursor starting column to probe prompt-length offset sensitivity.
func TestPTYShellPromptWrapCursorColumnVariants(t *testing.T) {
	cursorCols := []int{2, 8, 16, 22}
	for _, cursorCol := range cursorCols {
		cursorCol := cursorCol
		t.Run(fmt.Sprintf("col_%d", cursorCol), func(t *testing.T) {
			h := startShellE2EWithConfig(t, 24, 24, 1, cursorCol)
			defer h.close()

			steps := bytesToKeySteps("Help")
			steps = appendStepRepeat(steps, []byte("\x1b[D"), 3)
			steps = append(steps, []byte(strings.Repeat("z", 18)))
			steps = appendStepRepeat(steps, []byte("\x7f"), 18)
			steps = append(steps, []byte("\r"))

			out, err := h.runCaseSteps(steps, "__BF_E2E_DONE_WRAP_COLVAR__", 1, 20*time.Second)
			if err != nil {
				t.Fatalf("runCase failed: %v", err)
			}
			clean := sanitizeTTYString(out)
			if !strings.Contains(clean, "You're using the Butterfish Shell Mode") {
				t.Fatalf("column variant did not resolve to Help")
			}
		})
	}
}

// Bracketed paste in prompt mode mid-edit (real terminals often send this).
func TestPTYShellPromptBracketedPasteMidEdit(t *testing.T) {
	h := startShellE2EWithConfig(t, 24, 24, 1, 22)
	defer h.close()

	insert := strings.Repeat("p", 12)
	steps := bytesToKeySteps("Help")
	steps = appendStepRepeat(steps, []byte("\x1b[D"), 2)
	steps = append(steps, bracketedPaste(insert))
	steps = appendStepRepeat(steps, []byte("\x7f"), len(insert))
	steps = append(steps, []byte("\r"))

	out, err := h.runCaseSteps(steps, "__BF_E2E_DONE_BPASTE_PROMPT__", 1, 20*time.Second)
	if err != nil {
		t.Fatalf("runCase failed: %v", err)
	}
	clean := sanitizeTTYString(out)
	if !strings.Contains(clean, "You're using the Butterfish Shell Mode") {
		t.Fatalf("bracketed paste prompt edit did not resolve to Help")
	}
}

// Bracketed paste in shell command mode mid-edit.
func TestPTYShellCommandBracketedPasteMidEdit(t *testing.T) {
	h := startShellE2EWithConfig(t, 24, 24, 1, 22)
	defer h.close()

	base := strings.Repeat("ab", 18)
	insert := strings.Repeat("Q", 12)
	expected := base[:len(base)-8] + insert + base[len(base)-8:]

	steps := bytesToKeySteps("echo " + base)
	steps = appendStepRepeat(steps, []byte("\x1b[D"), 8)
	steps = append(steps, bracketedPaste(insert))
	steps = append(steps, []byte("\r"))

	out, err := h.runCaseSteps(steps, "__BF_E2E_DONE_BPASTE_SHELL__", 1, 20*time.Second)
	if err != nil {
		t.Fatalf("runCase failed: %v", err)
	}
	clean := sanitizeTTYString(out)
	if !strings.Contains(clean, expected) {
		t.Fatalf("bracketed paste shell edit output mismatch")
	}
}
