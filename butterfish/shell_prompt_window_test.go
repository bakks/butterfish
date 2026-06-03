package butterfish

import (
	"context"
	"strings"
	"testing"

	"github.com/bakks/butterfish/util"
	"github.com/bakks/tiktoken-go"
)

func mustTestEncoder(t *testing.T) *tiktoken.Tiktoken {
	t.Helper()

	encoder, err := encodingForModelOrDefault("gpt-5.5", DEFAULT_PROMPT_ENCODER)
	if err != nil {
		t.Fatalf("get test encoder: %v", err)
	}
	return encoder
}

func TestShellPromptWindowForModel(t *testing.T) {
	t.Run("gpt-5 default bumps to 64k", func(t *testing.T) {
		got := shellPromptWindowForModel("gpt-5.5", defaultShellMaxPromptTokens)
		if got != gpt5ShellMaxPromptTokens {
			t.Fatalf("expected %d, got %d", gpt5ShellMaxPromptTokens, got)
		}
	})

	t.Run("non-gpt-5 stays at configured default", func(t *testing.T) {
		got := shellPromptWindowForModel("gpt-4o", defaultShellMaxPromptTokens)
		if got != defaultShellMaxPromptTokens {
			t.Fatalf("expected %d, got %d", defaultShellMaxPromptTokens, got)
		}
	})

	t.Run("explicit lower max is respected", func(t *testing.T) {
		got := shellPromptWindowForModel("gpt-5.5", 8000)
		if got != 8000 {
			t.Fatalf("expected 8000, got %d", got)
		}
	})
}

func TestShellResponseTokenReserve(t *testing.T) {
	t.Run("uses default reserve when API cap is omitted", func(t *testing.T) {
		state := &ShellState{
			Butterfish: &ButterfishCtx{Config: &ButterfishConfig{}},
		}
		if got := state.shellResponseTokenReserve(); got != defaultShellResponseTokenReserve {
			t.Fatalf("expected default reserve %d, got %d", defaultShellResponseTokenReserve, got)
		}
	})

	t.Run("uses explicit response cap as reserve", func(t *testing.T) {
		state := &ShellState{
			Butterfish: &ButterfishCtx{Config: &ButterfishConfig{ShellMaxResponseTokens: 4096}},
		}
		if got := state.shellResponseTokenReserve(); got != 4096 {
			t.Fatalf("expected explicit reserve 4096, got %d", got)
		}
	})
}

func TestRequestCancelableAutosuggestUsesResponseTokenReserve(t *testing.T) {
	llm := &recordingLLM{
		completionResponse: &util.CompletionResponse{Completion: " suggestion"},
	}
	ch := make(chan *AutosuggestResult, 1)

	RequestCancelableAutosuggest(
		context.Background(),
		0,
		"git",
		"complete {command} using {history}",
		llm,
		DefaultAutosuggestModel,
		DefaultServiceTier,
		false,
		NewShellHistory(),
		1024,
		ch,
		mustTestEncoder(t),
	)

	if len(llm.completionRequests) != 1 {
		t.Fatalf("expected one completion request, got %d", len(llm.completionRequests))
	}
	if got := llm.completionRequests[0].MaxTokens; got != autosuggestResponseTokenReserve {
		t.Fatalf("expected autosuggest max tokens %d, got %d", autosuggestResponseTokenReserve, got)
	}
	if got := llm.completionRequests[0].Model; got != DefaultAutosuggestModel {
		t.Fatalf("expected autosuggest model %q, got %q", DefaultAutosuggestModel, got)
	}
	if got := llm.completionRequests[0].ReasoningEffort; got != DefaultAutosuggestReasoningEffort {
		t.Fatalf("expected autosuggest reasoning effort %q, got %q", DefaultAutosuggestReasoningEffort, got)
	}
	if got := llm.completionRequests[0].ServiceTier; got != DefaultServiceTier {
		t.Fatalf("expected autosuggest service tier %q, got %q", DefaultServiceTier, got)
	}
}

func TestNumTokensForModelGPT55(t *testing.T) {
	got := NumTokensForModel("gpt-5.5")
	if got != 1050000 {
		t.Fatalf("expected 1050000, got %d", got)
	}
}

func TestEncodingForModelOrDefaultGPT55(t *testing.T) {
	encoder, err := encodingForModelOrDefault("gpt-5.5", DEFAULT_PROMPT_ENCODER)
	if err != nil {
		t.Fatalf("expected fallback encoder for gpt-5.5, got error: %v", err)
	}
	if encoder == nil {
		t.Fatal("expected fallback encoder for gpt-5.5")
	}
}

func TestSupportsShellToolModel(t *testing.T) {
	if !supportsShellToolModel("gpt-5.5") {
		t.Fatal("expected gpt-5.5 to support shell tool")
	}
	if supportsShellToolModel("gpt-5") {
		t.Fatal("did not expect gpt-5 to support shell tool")
	}
}

func TestParsePS1UsesAgentModeIcons(t *testing.T) {
	input := "before " + PROMPT_PREFIX + EMOJI_DEFAULT + " 0" + PROMPT_SUFFIX + " after"

	safeState := &ShellState{
		Butterfish:  &ButterfishCtx{Config: &ButterfishConfig{}},
		SpecialMode: true,
	}
	_, _, safeCleaned := safeState.ParsePS1(input)
	if !strings.Contains(safeCleaned, EMOJI_AGENT) {
		t.Fatalf("expected safe agent icon %q in %q", EMOJI_AGENT, safeCleaned)
	}

	unsafeState := &ShellState{
		Butterfish:        &ButterfishCtx{Config: &ButterfishConfig{}},
		SpecialMode:       true,
		SpecialModeUnsafe: true,
	}
	_, _, unsafeCleaned := unsafeState.ParsePS1(input)
	if !strings.Contains(unsafeCleaned, EMOJI_AGENT_UNSAFE) {
		t.Fatalf("expected unsafe agent icon %q in %q", EMOJI_AGENT_UNSAFE, unsafeCleaned)
	}
}
