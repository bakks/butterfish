package butterfish

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/bakks/butterfish/prompt"
	"github.com/bakks/butterfish/util"
)

type errorLLM struct {
	err error
}

func (l errorLLM) CompletionStream(request *util.CompletionRequest, writer io.Writer) (*util.CompletionResponse, error) {
	return nil, l.err
}

func (l errorLLM) Completion(request *util.CompletionRequest) (*util.CompletionResponse, error) {
	return nil, l.err
}

type agentRetryLLM struct {
	requests chan *util.CompletionRequest
}

func (l agentRetryLLM) CompletionStream(request *util.CompletionRequest, writer io.Writer) (*util.CompletionResponse, error) {
	l.requests <- request
	return &util.CompletionResponse{}, nil
}

func (l agentRetryLLM) Completion(request *util.CompletionRequest) (*util.CompletionResponse, error) {
	return &util.CompletionResponse{}, nil
}

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

func TestAppendShellCallOutputStoresTruncatedOutput(t *testing.T) {
	history := NewShellHistory()
	history.AppendShellCallOutput(&util.ShellCallOutput{
		CallID:          "call_1",
		MaxOutputLength: 128,
		Output: []util.ShellCallOutputItem{
			{
				Stdout: strings.Repeat("x", 512) + "tail",
				Outcome: util.ShellCallOutcome{
					Type:     "exit",
					ExitCode: 0,
				},
			},
		},
	})

	if len(history.Blocks) != 1 {
		t.Fatalf("expected one history block, got %d", len(history.Blocks))
	}
	output := history.Blocks[0].ShellCallOutput
	if output == nil {
		t.Fatal("expected shell call output")
	}
	stdout := output.Output[0].Stdout
	if len(stdout) > 128 {
		t.Fatalf("expected stdout to be capped at 128 bytes, got %d", len(stdout))
	}
	if !strings.Contains(stdout, "butterfish truncated") {
		t.Fatalf("expected truncation marker, got %q", stdout)
	}
	if !strings.HasSuffix(stdout, "tail") {
		t.Fatalf("expected output tail to be preserved, got %q", stdout)
	}
}

func TestCompletionRoutineMarksDisplayedErrors(t *testing.T) {
	writer := &bytes.Buffer{}
	outputChan := make(chan *util.CompletionResponse, 1)

	CompletionRoutine(
		&util.CompletionRequest{},
		errorLLM{err: errors.New("boom")},
		writer,
		outputChan,
		"normal:",
		"error:",
		nil,
	)

	output := <-outputChan
	if output == nil {
		t.Fatal("expected completion output")
	}
	if output.Error != "boom" {
		t.Fatalf("unexpected error: %q", output.Error)
	}
	if !output.ErrorDisplayed {
		t.Fatal("expected error to be marked as displayed")
	}
	if !strings.Contains(writer.String(), "Error prompting LLM: boom") {
		t.Fatalf("expected LLM error to be written, got %q", writer.String())
	}
}

func TestAgentModeFunctionSuppressesAlreadyDisplayedError(t *testing.T) {
	promptOut := &bytes.Buffer{}
	state := &ShellState{
		Butterfish:              &ButterfishCtx{Config: &ButterfishConfig{}},
		PromptAgentAnswerWriter: promptOut,
		PromptAnswerWriter:      promptOut,
		History:                 NewShellHistory(),
		Color:                   DarkShellColorScheme,
		SpecialMode:             true,
		SpecialModeType:         specialModeAgent,
	}

	state.AgentModeFunction(&util.CompletionResponse{Error: "boom", ErrorDisplayed: true})

	if strings.Contains(promptOut.String(), "Agent mode error") {
		t.Fatalf("did not expect duplicate agent mode error, got %q", promptOut.String())
	}
	if state.SpecialMode {
		t.Fatal("expected agent mode to clear after error")
	}
}

func TestAgentModeFunctionDisplaysModeErrors(t *testing.T) {
	promptOut := &bytes.Buffer{}
	state := &ShellState{
		Butterfish:              &ButterfishCtx{Config: &ButterfishConfig{}},
		PromptAgentAnswerWriter: promptOut,
		PromptAnswerWriter:      promptOut,
		History:                 NewShellHistory(),
		Color:                   DarkShellColorScheme,
		SpecialMode:             true,
		SpecialModeType:         specialModeAgent,
	}

	state.AgentModeFunction(&util.CompletionResponse{Error: "mode-specific boom"})

	if !strings.Contains(promptOut.String(), "Agent mode error: mode-specific boom") {
		t.Fatalf("expected agent mode error, got %q", promptOut.String())
	}
	if state.SpecialMode {
		t.Fatal("expected agent mode to clear after error")
	}
}

func TestAgentModePromptErrorRetriesWithoutClearingMode(t *testing.T) {
	promptOut := &bytes.Buffer{}
	requests := make(chan *util.CompletionRequest, 1)
	config := MakeButterfishConfig()
	config.ShellPromptModel = BestCompletionModel
	state := &ShellState{
		Butterfish: &ButterfishCtx{
			Config:    config,
			LLMClient: agentRetryLLM{requests: requests},
			PromptLibrary: agentModePromptLibrary{
				prompts: map[string]string{
					prompt.AgentModeSystemMessage: "agent prompt for {goal} on {sysinfo}",
				},
			},
		},
		PromptAgentAnswerWriter: promptOut,
		PromptAnswerWriter:      promptOut,
		PromptOutputChan:        make(chan *util.CompletionResponse, 1),
		History:                 NewShellHistory(),
		Color:                   DarkShellColorScheme,
		SpecialMode:             true,
		SpecialModeType:         specialModeAgent,
		SpecialModeGoal:         "fix the repo",
		PromptMaxTokens:         gpt5ShellMaxPromptTokens,
	}

	retried := state.retryAgentModePromptError(&util.CompletionResponse{Error: "http2: response body closed"})
	if !retried {
		t.Fatal("expected agent mode error to be retried")
	}
	if !state.SpecialMode || !state.isAgentMode() {
		t.Fatal("expected agent mode to remain active")
	}
	if state.AgentModePromptErrorTries != 1 {
		t.Fatalf("expected one retry, got %d", state.AgentModePromptErrorTries)
	}
	if len(state.History.Blocks) != 0 {
		t.Fatalf("expected retry not to append error history, got %d blocks", len(state.History.Blocks))
	}
	if !strings.Contains(promptOut.String(), "Retrying agent mode request after LLM error (1/2).") {
		t.Fatalf("expected retry notice, got %q", promptOut.String())
	}

	select {
	case req := <-requests:
		if req.Prompt != "" {
			t.Fatalf("expected retry prompt to be empty continuation, got %q", req.Prompt)
		}
	case <-time.After(time.Second):
		t.Fatal("expected retry prompt request")
	}
}

func TestAgentModePromptUsesShellToolOnlyInUnsafeMode(t *testing.T) {
	promptOut := &bytes.Buffer{}
	config := MakeButterfishConfig()
	config.ShellPromptModel = BestCompletionModel
	promptLibrary := agentModePromptLibrary{
		prompts: map[string]string{
			prompt.AgentModeSystemMessage: "agent prompt for {goal} on {sysinfo}",
		},
	}

	safeRequests := make(chan *util.CompletionRequest, 1)
	safeState := &ShellState{
		Butterfish: &ButterfishCtx{
			Config:        config,
			LLMClient:     agentRetryLLM{requests: safeRequests},
			PromptLibrary: promptLibrary,
		},
		PromptAgentAnswerWriter: promptOut,
		PromptAnswerWriter:      promptOut,
		PromptOutputChan:        make(chan *util.CompletionResponse, 1),
		History:                 NewShellHistory(),
		Color:                   DarkShellColorScheme,
		SpecialMode:             true,
		SpecialModeType:         specialModeAgent,
		SpecialModeGoal:         "fix the repo",
		PromptMaxTokens:         gpt5ShellMaxPromptTokens,
	}
	safeState.agentModePrompt("Start now.")

	select {
	case req := <-safeRequests:
		if len(req.Tools) != 0 {
			t.Fatalf("expected safe agent mode not to use shell tool, got %#v", req.Tools)
		}
	case <-time.After(time.Second):
		t.Fatal("expected safe agent prompt request")
	}

	unsafeRequests := make(chan *util.CompletionRequest, 1)
	unsafeState := &ShellState{
		Butterfish: &ButterfishCtx{
			Config:        config,
			LLMClient:     agentRetryLLM{requests: unsafeRequests},
			PromptLibrary: promptLibrary,
		},
		PromptAgentAnswerWriter: promptOut,
		PromptAnswerWriter:      promptOut,
		PromptOutputChan:        make(chan *util.CompletionResponse, 1),
		History:                 NewShellHistory(),
		Color:                   DarkShellColorScheme,
		SpecialMode:             true,
		SpecialModeType:         specialModeAgent,
		SpecialModeUnsafe:       true,
		SpecialModeGoal:         "fix the repo",
		PromptMaxTokens:         gpt5ShellMaxPromptTokens,
	}
	unsafeState.agentModePrompt("Start now.")

	select {
	case req := <-unsafeRequests:
		if len(req.Tools) != 1 || req.Tools[0].Type != "shell" {
			t.Fatalf("expected unsafe agent mode to use shell tool, got %#v", req.Tools)
		}
	case <-time.After(time.Second):
		t.Fatal("expected unsafe agent prompt request")
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

	if got := childIn.String(); got != "echo one\necho two\n" {
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

func TestAgentModeFinishSuccessExitsSilently(t *testing.T) {
	promptOut := &bytes.Buffer{}
	state := &ShellState{
		Butterfish:              &ButterfishCtx{Config: &ButterfishConfig{}},
		PromptAgentAnswerWriter: promptOut,
		PromptAnswerWriter:      promptOut,
		History:                 NewShellHistory(),
		Color:                   DarkShellColorScheme,
		SpecialMode:             true,
		SpecialModeType:         specialModeAgent,
		ActiveFunction:          "finish",
		ActiveFunctionCallID:    "call_1",
	}

	resp := &util.CompletionResponse{
		FunctionName:       "finish",
		FunctionParameters: `{"success":true}`,
	}

	state.AgentModeFunction(resp)

	if state.SpecialMode {
		t.Fatal("expected agent mode to exit on successful finish")
	}
	if got := promptOut.String(); got != "" {
		t.Fatalf("expected no success exit message, got %q", got)
	}
	if len(state.History.Blocks) != 1 {
		t.Fatalf("expected one history block, got %d", len(state.History.Blocks))
	}
	block := state.History.Blocks[0]
	if block.Type != historyTypeFunctionOutput {
		t.Fatalf("expected function output history, got %d", block.Type)
	}
	if block.FunctionName != "finish" || block.ToolCallID != "call_1" {
		t.Fatalf("unexpected history block: %#v", block)
	}
}

func TestAgentModeFinishFailurePrintsExitMessage(t *testing.T) {
	promptOut := &bytes.Buffer{}
	state := &ShellState{
		Butterfish:              &ButterfishCtx{Config: &ButterfishConfig{}},
		PromptAgentAnswerWriter: promptOut,
		PromptAnswerWriter:      promptOut,
		History:                 NewShellHistory(),
		Color:                   DarkShellColorScheme,
		SpecialMode:             true,
		SpecialModeType:         specialModeAgent,
		ActiveFunction:          "finish",
		ActiveFunctionCallID:    "call_1",
	}

	resp := &util.CompletionResponse{
		FunctionName:       "finish",
		FunctionParameters: `{"success":false}`,
	}

	state.AgentModeFunction(resp)

	if state.SpecialMode {
		t.Fatal("expected agent mode to exit on failed finish")
	}
	if !strings.Contains(promptOut.String(), "Exited agent mode with FAILURE.") {
		t.Fatalf("expected failure exit message, got %q", promptOut.String())
	}
}
