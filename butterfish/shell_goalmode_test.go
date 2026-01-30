package butterfish

import (
	"bytes"
	"testing"

	"github.com/bakks/butterfish/util"
)

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
}

func TestGoalModeFunctionShellCalls_AcksExtraAndUsesNewlineCommands(t *testing.T) {
	childIn := &bytes.Buffer{}
	promptOut := &bytes.Buffer{}
	state := &ShellState{
		Butterfish:             &ButterfishCtx{Config: &ButterfishConfig{}},
		ChildIn:                childIn,
		PromptGoalAnswerWriter: promptOut,
		PromptAnswerWriter:     promptOut,
		History:                NewShellHistory(),
	}

	resp := &util.CompletionResponse{
		ShellCalls: []*util.ShellCall{
			{CallID: "call_1", Commands: []string{"echo one", "echo two"}},
			{CallID: "call_2", Commands: []string{"pwd"}},
		},
	}

	state.GoalModeFunction(resp)

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
}
