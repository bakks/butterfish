package butterfish

import (
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRealtimeWebSocketURL(t *testing.T) {
	t.Parallel()

	got, err := realtimeWebSocketURL("https://api.openai.com/v1/responses", "gpt-realtime-1.5")
	require.NoError(t, err)
	assert.Equal(t, "wss://api.openai.com/v1/realtime?model=gpt-realtime-1.5", got)

	got, err = realtimeWebSocketURL("http://localhost:8080/v1/responses", "demo")
	require.NoError(t, err)
	assert.Equal(t, "ws://localhost:8080/v1/realtime?model=demo", got)
}

func TestNormalizeHotkeyString(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "p", normalizeHotkeyString("P"))
	assert.Equal(t, "x", normalizeHotkeyString(" xyz "))
	assert.Equal(t, "", normalizeHotkeyString(""))
}

func TestMicrophoneCaptureArgs(t *testing.T) {
	t.Parallel()

	old := os.Getenv("BUTTERFISH_VOICE_INPUT")
	t.Cleanup(func() {
		if old == "" {
			_ = os.Unsetenv("BUTTERFISH_VOICE_INPUT")
		} else {
			_ = os.Setenv("BUTTERFISH_VOICE_INPUT", old)
		}
	})

	_ = os.Setenv("BUTTERFISH_VOICE_INPUT", "custom-input")

	args, err := microphoneCaptureArgs()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		require.Error(t, err)
		return
	}

	require.NoError(t, err)
	assert.Contains(t, args, "custom-input")
	assert.Contains(t, args, "-ar")
	assert.Contains(t, args, "24000")
}

func TestMakeSessionUpdate(t *testing.T) {
	t.Parallel()

	cfg := MakeButterfishConfig()
	cfg.VoiceInstructions = "test voice instructions"
	cfg.VoiceVoice = "cedar"

	event := makeSessionUpdate(cfg)
	assert.Equal(t, "session.update", event.Type)
	assert.Equal(t, "realtime", event.Session["type"])
	assert.Equal(t, "test voice instructions", event.Session["instructions"])

	audio := event.Session["audio"].(map[string]any)
	output := audio["output"].(map[string]any)
	assert.Equal(t, "cedar", output["voice"])
}
