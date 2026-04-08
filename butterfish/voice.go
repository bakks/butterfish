package butterfish

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/gorilla/websocket"
	"golang.org/x/term"
)

type realtimeClientEvent struct {
	Type    string `json:"type"`
	EventID string `json:"event_id,omitempty"`
}

type realtimeSessionUpdateEvent struct {
	Type    string         `json:"type"`
	Session map[string]any `json:"session"`
	EventID string         `json:"event_id,omitempty"`
}

type realtimeInputAudioAppendEvent struct {
	Type  string `json:"type"`
	Audio string `json:"audio"`
}

type realtimeSimpleEvent struct {
	Type string `json:"type"`
}

type realtimeOutputAudioDeltaEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
}

type realtimeTranscriptEvent struct {
	Type       string `json:"type"`
	Transcript string `json:"transcript"`
}

type realtimeTranscriptDeltaEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
}

type realtimeErrorEvent struct {
	Type  string `json:"type"`
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

type realtimeWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (w *realtimeWriter) WriteJSON(v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteJSON(v)
}

type pcmPlayer struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	mu    sync.Mutex
}

func RunVoice(ctx context.Context, config *ButterfishConfig) error {
	if config.OpenAIToken == "" {
		return errors.New("OPENAI_API_KEY is required for voice mode")
	}

	if err := requireExecutable("ffmpeg"); err != nil {
		return err
	}
	if err := requireExecutable("ffplay"); err != nil {
		return err
	}

	wsURL, err := realtimeWebSocketURL(config.BaseURL, config.VoiceModel)
	if err != nil {
		return err
	}

	header := http.Header{}
	header.Set("Authorization", "Bearer "+config.OpenAIToken)

	dialer := websocket.Dialer{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}

	conn, _, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		return fmt.Errorf("connect realtime websocket: %w", err)
	}
	defer conn.Close()

	writer := &realtimeWriter{conn: conn}
	player, err := startPCMPlayer(ctx)
	if err != nil {
		return err
	}
	defer player.Close()

	micCmd, micStdout, err := startMicrophoneCapture(ctx)
	if err != nil {
		return err
	}
	defer stopCommand(micCmd)

	if err := writer.WriteJSON(makeSessionUpdate(config)); err != nil {
		return fmt.Errorf("send session.update: %w", err)
	}
	paused := atomic.Bool{}
	paused.Store(false)
	done := make(chan error, 3)
	assistantStarted := atomic.Bool{}

	fmt.Printf("Voice mode connected.\n")
	fmt.Printf("Hotkeys: %s pause/unpause, %s quit\n", displayHotkey(config.VoicePauseKey), displayHotkey(config.VoiceQuitKey))

	go func() {
		done <- runRealtimeReadLoop(conn, player, &assistantStarted)
	}()

	go func() {
		done <- streamMicrophone(micStdout, writer, &paused)
	}()

	go func() {
		done <- captureVoiceHotkeys(ctx, &paused, writer, player, config.VoicePauseKey, config.VoiceQuitKey)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	}
}

func runRealtimeReadLoop(conn *websocket.Conn, player *pcmPlayer, assistantStarted *atomic.Bool) error {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var event realtimeClientEvent
		if err := json.Unmarshal(data, &event); err != nil {
			continue
		}

		switch event.Type {
		case "response.output_audio.delta":
			var delta realtimeOutputAudioDeltaEvent
			if err := json.Unmarshal(data, &delta); err != nil {
				return err
			}
			audio, err := base64.StdEncoding.DecodeString(delta.Delta)
			if err != nil {
				return err
			}
			if !assistantStarted.Swap(true) {
				fmt.Printf("\nButterfish: ")
			}
			if err := player.Write(audio); err != nil {
				return err
			}
		case "response.output_audio_transcript.delta":
			var delta realtimeTranscriptDeltaEvent
			if err := json.Unmarshal(data, &delta); err != nil {
				return err
			}
			fmt.Printf("%s", delta.Delta)
		case "response.done":
			if assistantStarted.Swap(false) {
				fmt.Printf("\n")
			}
		case "conversation.item.input_audio_transcription.completed":
			var transcript realtimeTranscriptEvent
			if err := json.Unmarshal(data, &transcript); err != nil {
				return err
			}
			if strings.TrimSpace(transcript.Transcript) != "" {
				fmt.Printf("\nYou: %s\n", transcript.Transcript)
			}
		case "input_audio_buffer.speech_started":
			player.Reset()
		case "error":
			var apiErr realtimeErrorEvent
			if err := json.Unmarshal(data, &apiErr); err != nil {
				return err
			}
			return fmt.Errorf("realtime API error (%s/%s): %s", apiErr.Error.Type, apiErr.Error.Code, apiErr.Error.Message)
		}
	}
}

func streamMicrophone(reader io.Reader, writer *realtimeWriter, paused *atomic.Bool) error {
	bufReader := bufio.NewReaderSize(reader, 32*1024)
	chunk := make([]byte, 4096)

	for {
		n, err := bufReader.Read(chunk)
		if n > 0 && !paused.Load() {
			payload := base64.StdEncoding.EncodeToString(chunk[:n])
			if err := writer.WriteJSON(realtimeInputAudioAppendEvent{
				Type:  "input_audio_buffer.append",
				Audio: payload,
			}); err != nil {
				return err
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func captureVoiceHotkeys(ctx context.Context, paused *atomic.Bool, writer *realtimeWriter, player *pcmPlayer, pauseKey, quitKey string) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("voice mode requires an interactive terminal for hotkeys")
	}

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	reader := bufio.NewReader(os.Stdin)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		b, err := reader.ReadByte()
		if err != nil {
			return err
		}

		switch normalizeHotkeyByte(b) {
		case normalizeHotkeyString(pauseKey):
			newPaused := !paused.Load()
			paused.Store(newPaused)
			if newPaused {
				player.Reset()
				if err := writer.WriteJSON(realtimeSimpleEvent{Type: "input_audio_buffer.clear"}); err != nil {
					return err
				}
				fmt.Printf("\n[paused]\n")
			} else {
				fmt.Printf("\n[listening]\n")
			}
		case normalizeHotkeyString(quitKey):
			return context.Canceled
		}
	}
}

func makeSessionUpdate(config *ButterfishConfig) realtimeSessionUpdateEvent {
	return realtimeSessionUpdateEvent{
		Type: "session.update",
		Session: map[string]any{
			"type":         "realtime",
			"instructions": strings.TrimSpace(config.VoiceInstructions),
			"audio": map[string]any{
				"input": map[string]any{
					"turn_detection": map[string]any{
						"type":               "server_vad",
						"create_response":    true,
						"interrupt_response": true,
					},
					"transcription": map[string]any{
						"model": "gpt-4o-mini-transcribe",
					},
				},
				"output": map[string]any{
					"voice": config.VoiceVoice,
				},
			},
		},
	}
}

func realtimeWebSocketURL(baseURL, model string) (string, error) {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		base = "https://api.openai.com/v1/responses"
	}

	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse base url: %w", err)
	}

	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "wss", "ws":
	default:
		return "", fmt.Errorf("unsupported base url scheme %q", u.Scheme)
	}

	path := strings.TrimSuffix(u.Path, "/")
	path = strings.TrimSuffix(path, "/responses")
	path = strings.TrimSuffix(path, "/chat/completions")
	if path == "" {
		path = "/v1"
	}
	if !strings.HasSuffix(path, "/v1") {
		path = strings.TrimRight(path, "/")
	}
	u.Path = strings.TrimRight(path, "/") + "/realtime"
	q := u.Query()
	q.Set("model", model)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func requireExecutable(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("%s is required for voice mode", name)
	}
	return nil
}

func startMicrophoneCapture(ctx context.Context) (*exec.Cmd, io.ReadCloser, error) {
	args, err := microphoneCaptureArgs()
	if err != nil {
		return nil, nil, err
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd, stdout, nil
}

func microphoneCaptureArgs() ([]string, error) {
	override := strings.TrimSpace(os.Getenv("BUTTERFISH_VOICE_INPUT"))
	switch runtime.GOOS {
	case "darwin":
		input := ":0"
		if override != "" {
			input = override
		}
		return []string{
			"-hide_banner", "-loglevel", "error",
			"-f", "avfoundation",
			"-i", input,
			"-ac", "1",
			"-ar", "24000",
			"-f", "s16le",
			"-",
		}, nil
	case "linux":
		input := "default"
		format := "pulse"
		if override != "" {
			input = override
		}
		return []string{
			"-hide_banner", "-loglevel", "error",
			"-f", format,
			"-i", input,
			"-ac", "1",
			"-ar", "24000",
			"-f", "s16le",
			"-",
		}, nil
	default:
		return nil, fmt.Errorf("voice mode microphone capture is not implemented for %s", runtime.GOOS)
	}
}

func startPCMPlayer(ctx context.Context) (*pcmPlayer, error) {
	cmd := exec.CommandContext(ctx, "ffplay",
		"-nodisp",
		"-loglevel", "quiet",
		"-fflags", "nobuffer",
		"-flags", "low_delay",
		"-f", "s16le",
		"-ar", "24000",
		"-ac", "1",
		"-",
	)
	cmd.Stderr = io.Discard
	cmd.Stdout = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &pcmPlayer{
		cmd:   cmd,
		stdin: stdin,
	}, nil
}

func (p *pcmPlayer) Write(data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p == nil || p.stdin == nil {
		return errors.New("audio player is not available")
	}

	if _, err := p.stdin.Write(data); err != nil {
		if !isBrokenPipe(err) {
			return err
		}
		if resetPCMPlayerLocked(p) != nil {
			return err
		}
		_, retryErr := p.stdin.Write(data)
		return retryErr
	}

	return nil
}

func (p *pcmPlayer) Reset() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return resetPCMPlayerLocked(p)
}

func (p *pcmPlayer) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	return stopCommand(p.cmd)
}

func resetPCMPlayerLocked(p *pcmPlayer) error {
	if err := stopCommand(p.cmd); err != nil {
		return err
	}
	next, err := startPCMPlayer(context.Background())
	if err != nil {
		return err
	}
	p.cmd = next.cmd
	p.stdin = next.stdin
	return nil
}

func stopCommand(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return nil
}

func isBrokenPipe(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EPIPE) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "broken pipe")
}

func normalizeHotkeyByte(b byte) string {
	return strings.ToLower(string([]byte{b}))
}

func normalizeHotkeyString(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return ""
	}
	return string([]byte{s[0]})
}

func displayHotkey(s string) string {
	normalized := normalizeHotkeyString(s)
	if normalized == "" {
		return "?"
	}
	return normalized
}
