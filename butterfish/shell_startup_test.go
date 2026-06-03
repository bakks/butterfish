package butterfish

import (
	"strings"
	"testing"
	"time"
)

func TestClearStartupShellOutputWaitsForPromptSuffix(t *testing.T) {
	ch := make(chan *byteMsg, 4)
	done := make(chan struct {
		prompt    string
		sawPrompt bool
	})

	go func() {
		prompt, sawPrompt := clearStartupShellOutput(ch, time.Second, ps1FullRegex, EMOJI_DEFAULT)
		done <- struct {
			prompt    string
			sawPrompt bool
		}{prompt: prompt, sawPrompt: sawPrompt}
	}()

	ch <- &byteMsg{Data: []byte("~/repo > ")}
	ch <- &byteMsg{Data: []byte("PS1=$'%{\\033Q%}'$PS1$'fish%{ %?\\033R%} '\n")}
	ch <- &byteMsg{Data: []byte("rc output before prompt\n")}

	select {
	case result := <-done:
		t.Fatalf("startup drain returned before seeing the prompt suffix, result=%+v", result)
	case <-time.After(20 * time.Millisecond):
	}

	ch <- &byteMsg{Data: []byte(PROMPT_PREFIX + "~/repo > " + EMOJI_DEFAULT + " 0" + PROMPT_SUFFIX + " ")}

	select {
	case result := <-done:
		if !result.sawPrompt {
			t.Fatal("expected startup drain to report prompt suffix")
		}
		if result.prompt != "~/repo > "+EMOJI_DEFAULT+" " {
			t.Fatalf("unexpected replay prompt: %q", result.prompt)
		}
		if strings.Contains(result.prompt, "PS1=$") || strings.Contains(result.prompt, "rc output") || strings.Contains(result.prompt, PROMPT_SUFFIX) {
			t.Fatalf("replay prompt contains startup noise or marker: %q", result.prompt)
		}
	case <-time.After(time.Second):
		t.Fatal("startup drain did not return after seeing the prompt suffix")
	}
}

func TestClearStartupShellOutputTimesOutWithoutPromptSuffix(t *testing.T) {
	ch := make(chan *byteMsg, 1)
	done := make(chan struct {
		prompt    string
		sawPrompt bool
	})

	go func() {
		prompt, sawPrompt := clearStartupShellOutput(ch, 20*time.Millisecond, ps1FullRegex, EMOJI_DEFAULT)
		done <- struct {
			prompt    string
			sawPrompt bool
		}{prompt: prompt, sawPrompt: sawPrompt}
	}()

	ch <- &byteMsg{Data: []byte("startup output\n")}

	select {
	case result := <-done:
		if result.sawPrompt {
			t.Fatal("did not expect startup drain to report prompt suffix on timeout")
		}
		if result.prompt != "" {
			t.Fatalf("did not expect replay prompt on timeout, got %q", result.prompt)
		}
	case <-time.After(time.Second):
		t.Fatal("startup drain did not time out")
	}
}

func TestClearStartupShellOutputHandlesSplitPromptSuffix(t *testing.T) {
	ch := make(chan *byteMsg, 2)
	done := make(chan struct {
		prompt    string
		sawPrompt bool
	})

	go func() {
		prompt, sawPrompt := clearStartupShellOutput(ch, time.Second, ps1FullRegex, EMOJI_DEFAULT)
		done <- struct {
			prompt    string
			sawPrompt bool
		}{prompt: prompt, sawPrompt: sawPrompt}
	}()

	ch <- &byteMsg{Data: []byte(PROMPT_PREFIX + "~/repo > " + EMOJI_DEFAULT + " 0")}
	ch <- &byteMsg{Data: []byte(PROMPT_SUFFIX + " ")}

	select {
	case result := <-done:
		if !result.sawPrompt {
			t.Fatal("expected split prompt suffix to be detected")
		}
		if result.prompt != "~/repo > "+EMOJI_DEFAULT+" " {
			t.Fatalf("unexpected replay prompt: %q", result.prompt)
		}
	case <-time.After(time.Second):
		t.Fatal("startup drain did not return after split prompt suffix")
	}
}
