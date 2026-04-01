package butterfish

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/bakks/butterfish/prompt"
	"github.com/bakks/butterfish/util"
)

type agentModePromptLibrary struct {
	prompts map[string]string
}

func (p agentModePromptLibrary) GetPrompt(name string, args ...string) (string, error) {
	text, ok := p.prompts[name]
	if !ok {
		return "", errors.New("prompt not found")
	}
	return prompt.Interpolate(text, args...)
}

func (p agentModePromptLibrary) GetUninterpolatedPrompt(name string) (string, error) {
	text, ok := p.prompts[name]
	if !ok {
		return "", errors.New("prompt not found")
	}
	return text, nil
}

func (p agentModePromptLibrary) InterpolatePrompt(promptText string, args ...string) (string, error) {
	return prompt.Interpolate(promptText, args...)
}

func TestSkippedShellCallOutput(t *testing.T) {
	out := skippedShellCallOutput(&util.ShellCall{CallID: "call_1", MaxOutputLength: 123})
	if out == nil {
		t.Fatal("expected output")
	}
	if out.CallID != "call_1" {
		t.Fatalf("unexpected call id: %s", out.CallID)
	}
	if out.MaxOutputLength != 123 {
		t.Fatalf("unexpected max output length: %d", out.MaxOutputLength)
	}
	if len(out.Output) != 1 {
		t.Fatalf("unexpected output items: %d", len(out.Output))
	}
	if out.Output[0].Stderr == "" {
		t.Fatal("expected skipped message in stderr")
	}
	if out.Output[0].Outcome.ExitCode == 0 {
		t.Fatal("expected non-zero exit code for skipped call")
	}
}

func TestAgentModeFunctionShellCalls_AcksExtraAndUsesNewlineCommands(t *testing.T) {
	childIn := &bytes.Buffer{}
	promptOut := &bytes.Buffer{}
	state := &ShellState{
		Butterfish:              &ButterfishCtx{Config: &ButterfishConfig{}},
		ChildIn:                 childIn,
		PromptAgentAnswerWriter: promptOut,
		PromptAnswerWriter:      promptOut,
		History:                 NewShellHistory(),
	}

	resp := &util.CompletionResponse{
		ShellCalls: []*util.ShellCall{
			{CallID: "call_1", Commands: []string{"echo one", "echo two"}},
			{CallID: "call_2", Commands: []string{"pwd"}},
		},
	}

	state.AgentModeFunction(resp)

	if got := childIn.String(); got != "echo one\necho two" {
		t.Fatalf("unexpected command write: %q", got)
	}

	if len(state.History.Blocks) != 1 {
		t.Fatalf("expected one history block, got %d", len(state.History.Blocks))
	}
	block := state.History.Blocks[0]
	if block.Type != historyTypeToolOutput {
		t.Fatalf("unexpected history type: %d", block.Type)
	}
	if block.ToolCallID != "call_2" {
		t.Fatalf("unexpected tool call id: %s", block.ToolCallID)
	}
	if block.ShellCallOutput == nil || len(block.ShellCallOutput.Output) != 1 {
		t.Fatal("expected shell_call_output for skipped call")
	}
	if block.ShellCallOutput.Output[0].Outcome.ExitCode == 0 {
		t.Fatal("expected skipped call to be non-successful")
	}
}

func TestGetAgentModeSystemPromptPrefersCustomizedLegacyPrompt(t *testing.T) {
	agentDefault, ok := prompt.DefaultPromptByName(prompt.AgentModeSystemMessage)
	if !ok {
		t.Fatal("expected default agent mode prompt")
	}

	state := &ShellState{
		Butterfish: &ButterfishCtx{
			PromptLibrary: agentModePromptLibrary{
				prompts: map[string]string{
					prompt.AgentModeSystemMessage:       agentDefault.Prompt,
					prompt.LegacyAgentModeSystemMessage: "legacy prompt for {goal} on {sysinfo}",
				},
			},
		},
		SpecialModeGoal: "fix the repo",
	}

	got, err := state.getAgentModeSystemPrompt()
	if err != nil {
		t.Fatalf("get agent mode system prompt: %v", err)
	}
	if !strings.Contains(got, "legacy prompt for fix the repo on") {
		t.Fatalf("expected legacy prompt to be used, got %q", got)
	}
}
