package butterfish

import (
	"strings"
	"testing"
	"time"
)

func TestLikelyTUIControlSequence(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{
			name: "plain text",
			data: []byte("hello world\n"),
			want: false,
		},
		{
			name: "color only sgr",
			data: []byte("\x1b[31mhello\x1b[0m"),
			want: false,
		},
		{
			name: "cursor movement",
			data: []byte("\x1b[2;4Hhello"),
			want: true,
		},
		{
			name: "clear line",
			data: []byte("\x1b[2K"),
			want: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := likelyTUIControlSequence(tc.data)
			if got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func TestHasRunningChildrenCached(t *testing.T) {
	calls := 0
	state := &ShellState{
		RunningChildrenCheckInterval: time.Hour,
		HasRunningChildrenFn: func() bool {
			calls++
			return true
		},
	}

	if !state.hasRunningChildrenCached(false) {
		t.Fatal("expected true on first call")
	}
	if calls != 1 {
		t.Fatalf("expected one check call, got %d", calls)
	}

	if !state.hasRunningChildrenCached(false) {
		t.Fatal("expected cached true")
	}
	if calls != 1 {
		t.Fatalf("expected cached result without extra calls, got %d", calls)
	}

	if !state.hasRunningChildrenCached(true) {
		t.Fatal("expected true after forced refresh")
	}
	if calls != 2 {
		t.Fatalf("expected second check call after force, got %d", calls)
	}
}

func TestAppendTUITailBoundedAndSanitized(t *testing.T) {
	state := &ShellState{TUITailMaxBytes: 16}

	state.appendTUITail([]byte("abc\x1b[2Jdef\r\n"))
	state.appendTUITail([]byte(strings.Repeat("Z", 32)))

	if len(state.TUITailBuffer) > 16 {
		t.Fatalf("tail exceeded max bytes: %d", len(state.TUITailBuffer))
	}

	for _, b := range state.TUITailBuffer {
		if b == 0x1b || b == '\r' {
			t.Fatalf("tail contains unsanitized control byte: %x", b)
		}
	}
}

func TestFlushTUITailToHistory(t *testing.T) {
	state := &ShellState{
		History:       NewShellHistory(),
		TUITailBuffer: []byte("line1\nline2"),
	}

	state.flushTUITailToHistory()

	if len(state.TUITailBuffer) != 0 {
		t.Fatal("expected tail buffer to be cleared")
	}
	if len(state.History.Blocks) != 1 {
		t.Fatalf("expected one history block, got %d", len(state.History.Blocks))
	}

	got := state.History.Blocks[0].Content.String()
	if !strings.Contains(got, "[interactive session tail]") {
		t.Fatalf("expected interactive tail marker, got %q", got)
	}
	if !strings.Contains(got, "line1") || !strings.Contains(got, "line2") {
		t.Fatalf("expected tail lines in history, got %q", got)
	}
}
