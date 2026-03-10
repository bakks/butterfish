package butterfish

import "testing"

func TestShellPromptWindowForModel(t *testing.T) {
	t.Run("gpt-5 default bumps to 64k", func(t *testing.T) {
		got := shellPromptWindowForModel("gpt-5.4", defaultShellMaxPromptTokens)
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
		got := shellPromptWindowForModel("gpt-5.4", 8000)
		if got != 8000 {
			t.Fatalf("expected 8000, got %d", got)
		}
	})
}

func TestNumTokensForModelGPT54(t *testing.T) {
	got := NumTokensForModel("gpt-5.4")
	if got != 1050000 {
		t.Fatalf("expected 1050000, got %d", got)
	}
}

func TestSupportsShellToolModel(t *testing.T) {
	if !supportsShellToolModel("gpt-5.4") {
		t.Fatal("expected gpt-5.4 to support shell tool")
	}
	if supportsShellToolModel("gpt-5") {
		t.Fatal("did not expect gpt-5 to support shell tool")
	}
}
