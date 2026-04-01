package butterfish

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/bakks/butterfish/prompt"
	"github.com/bakks/butterfish/util"
)

type actionModePromptLibrary struct{}

func (actionModePromptLibrary) GetPrompt(name string, args ...string) (string, error) {
	switch name {
	case prompt.ActionModeSystemMessage:
		return "action system prompt", nil
	default:
		return "system prompt", nil
	}
}

func (actionModePromptLibrary) GetUninterpolatedPrompt(name string) (string, error) {
	return "", nil
}

func (actionModePromptLibrary) InterpolatePrompt(prompt string, args ...string) (string, error) {
	return prompt, nil
}

func TestPromptColorForStringUsesActionModeColor(t *testing.T) {
	state := &ShellState{Color: DarkShellColorScheme}

	if got := state.promptColorForString("@show big files"); got != state.Color.PromptAction {
		t.Fatalf("expected action prompt color %q, got %q", state.Color.PromptAction, got)
	}
	if got := state.promptColorForString("@@show big files"); got != state.Color.PromptAgentUnsafe {
		t.Fatalf("expected auto action prompt color %q, got %q", state.Color.PromptAgentUnsafe, got)
	}
	if got := state.promptColorForString("!fix it"); got != state.Color.PromptAgent {
		t.Fatalf("expected agent prompt color %q, got %q", state.Color.PromptAgent, got)
	}
	if got := state.promptColorForString("!!fix it"); got != state.Color.PromptAgentUnsafe {
		t.Fatalf("expected unsafe agent prompt color %q, got %q", state.Color.PromptAgentUnsafe, got)
	}
}

func TestActionModeDefaultPromptMentionsShellHistory(t *testing.T) {
	p, ok := prompt.DefaultPromptByName(prompt.ActionModeSystemMessage)
	if !ok {
		t.Fatal("expected default action mode prompt")
	}
	if !strings.Contains(p.Prompt, "recent shell history") {
		t.Fatalf("expected action mode prompt to mention shell history, got %q", p.Prompt)
	}
	if !strings.Contains(p.Prompt, "try again") {
		t.Fatalf("expected action mode prompt to mention retry-style requests, got %q", p.Prompt)
	}
}

func TestShouldClearCompletedFunctionLineKeepsActionCommandPrompt(t *testing.T) {
	actionState := &ShellState{
		SpecialMode:     true,
		SpecialModeType: specialModeAction,
		ActiveFunction:  "command",
	}
	if actionState.shouldClearCompletedFunctionLine() {
		t.Fatal("expected action command completion to keep the shell prompt visible")
	}

	agentState := &ShellState{
		SpecialMode:     true,
		SpecialModeType: specialModeAgent,
		ActiveFunction:  "command",
	}
	if !agentState.shouldClearCompletedFunctionLine() {
		t.Fatal("expected agent mode to clear the completed function line")
	}
}

func TestParsePS1LeavesDefaultIconInActionMode(t *testing.T) {
	input := "before " + PROMPT_PREFIX + EMOJI_DEFAULT + " 0" + PROMPT_SUFFIX + " after"
	state := &ShellState{
		Butterfish:      &ButterfishCtx{Config: &ButterfishConfig{}},
		SpecialMode:     true,
		SpecialModeType: specialModeAction,
	}

	_, _, cleaned := state.ParsePS1(input)
	if !strings.Contains(cleaned, EMOJI_DEFAULT) {
		t.Fatalf("expected default icon %q in %q", EMOJI_DEFAULT, cleaned)
	}
	if strings.Contains(cleaned, EMOJI_AGENT) {
		t.Fatalf("did not expect agent icon %q in %q", EMOJI_AGENT, cleaned)
	}
}

func TestActionModeFunctionCommandStagesSingleCommand(t *testing.T) {
	childIn := &bytes.Buffer{}
	promptOut := &bytes.Buffer{}
	state := &ShellState{
		Butterfish:               &ButterfishCtx{Config: &ButterfishConfig{}},
		ChildIn:                  childIn,
		PromptActionAnswerWriter: promptOut,
		PromptAgentAnswerWriter:  promptOut,
		PromptAnswerWriter:       promptOut,
		History:                  NewShellHistory(),
		Color:                    DarkShellColorScheme,
		SpecialMode:              true,
		SpecialModeType:          specialModeAction,
		ActiveFunction:           "command",
		ActiveFunctionCallID:     "call_1",
	}

	resp := &util.CompletionResponse{
		FunctionName:       "command",
		FunctionParameters: `{"cmd":"ls -la"}`,
	}

	state.ActionModeFunction(resp)

	if got := childIn.String(); got != "ls -la" {
		t.Fatalf("unexpected command write: %q", got)
	}
	if !state.SpecialMode || !state.isActionMode() {
		t.Fatal("expected action mode to remain active until the command finishes")
	}
	if state.State != stateShell {
		t.Fatalf("expected stateShell, got %d", state.State)
	}
	if state.Command == nil || state.Command.String() != "ls -la" {
		t.Fatalf("expected staged command buffer, got %#v", state.Command)
	}
	if len(state.History.Blocks) != 1 {
		t.Fatalf("expected one history block, got %d", len(state.History.Blocks))
	}
	block := state.History.Blocks[0]
	if block.Type != historyTypeFunctionOutput {
		t.Fatalf("expected function output history, got %d", block.Type)
	}
	if block.ToolCallID != "call_1" {
		t.Fatalf("expected function output for call_1, got %q", block.ToolCallID)
	}
	if !strings.Contains(block.Content.String(), "Command staged in the shell for user review.") {
		t.Fatalf("unexpected function output history: %q", block.Content.String())
	}
}

func TestActionModeFunctionCommandAutoExecutesWhenUnsafe(t *testing.T) {
	childIn := &bytes.Buffer{}
	promptOut := &bytes.Buffer{}
	state := &ShellState{
		Butterfish:               &ButterfishCtx{Config: &ButterfishConfig{}},
		ChildIn:                  childIn,
		PromptActionAnswerWriter: promptOut,
		PromptAgentAnswerWriter:  promptOut,
		PromptAnswerWriter:       promptOut,
		History:                  NewShellHistory(),
		Color:                    DarkShellColorScheme,
		SpecialMode:              true,
		SpecialModeType:          specialModeAction,
		SpecialModeUnsafe:        true,
		ActiveFunction:           "command",
		ActiveFunctionCallID:     "call_1",
	}

	resp := &util.CompletionResponse{
		FunctionName:       "command",
		FunctionParameters: `{"cmd":"ls -la"}`,
	}

	state.ActionModeFunction(resp)

	if got := childIn.String(); got != "ls -la\n" {
		t.Fatalf("unexpected command write: %q", got)
	}
	if state.State != stateNormal {
		t.Fatalf("expected stateNormal, got %d", state.State)
	}
	if len(state.History.Blocks) != 2 {
		t.Fatalf("expected two history blocks, got %#v", state.History.Blocks)
	}
	if state.History.Blocks[0].Type != historyTypeFunctionOutput {
		t.Fatalf("expected function output history block first, got %#v", state.History.Blocks[0])
	}
	if !strings.Contains(state.History.Blocks[0].Content.String(), "Command sent to the shell for immediate execution.") {
		t.Fatalf("unexpected function output history: %q", state.History.Blocks[0].Content.String())
	}
	if state.History.Blocks[1].Type != historyTypeShellInput {
		t.Fatalf("expected shell input history block, got %#v", state.History.Blocks)
	}
	if got := state.History.Blocks[1].Content.String(); got != "ls -la" {
		t.Fatalf("unexpected shell input history: %q", got)
	}
}

func TestActionModeFunctionDeclineExitsMode(t *testing.T) {
	promptOut := &bytes.Buffer{}
	state := &ShellState{
		Butterfish:               &ButterfishCtx{Config: &ButterfishConfig{}},
		PromptActionAnswerWriter: promptOut,
		PromptAgentAnswerWriter:  promptOut,
		PromptAnswerWriter:       promptOut,
		History:                  NewShellHistory(),
		Color:                    DarkShellColorScheme,
		SpecialMode:              true,
		SpecialModeType:          specialModeAction,
		ActiveFunction:           "decline",
		ActiveFunctionCallID:     "call_1",
	}

	resp := &util.CompletionResponse{
		FunctionName:       "decline",
		FunctionParameters: `{"reason":"This needs more than one command."}`,
	}

	state.ActionModeFunction(resp)

	if state.SpecialMode {
		t.Fatal("expected action mode to exit on decline")
	}
	if !strings.Contains(promptOut.String(), "This needs more than one command.") {
		t.Fatalf("expected decline reason in output, got %q", promptOut.String())
	}
	if !strings.Contains(promptOut.String(), "Exited action mode.") {
		t.Fatalf("expected exit message in output, got %q", promptOut.String())
	}
	if len(state.History.Blocks) == 0 {
		t.Fatal("expected decline to be recorded in history")
	}
}

func TestActionModePromptUsesCustomPromptAndDeclineFunction(t *testing.T) {
	llm := &recordingLLM{streamResponse: &util.CompletionResponse{}}
	config := MakeButterfishConfig()
	config.ShellPromptModel = "gpt-5.4"
	config.ShellReasoningEffort = "medium"
	config.ShellMaxHistoryBlockTokens = 1024

	state := &ShellState{
		Butterfish: &ButterfishCtx{
			Config:        config,
			PromptLibrary: actionModePromptLibrary{},
			LLMClient:     llm,
		},
		PromptMaxTokens:          defaultShellMaxPromptTokens,
		History:                  NewShellHistory(),
		PromptOutputChan:         make(chan *util.CompletionResponse, 1),
		PromptActionAnswerWriter: &bytes.Buffer{},
		PromptAgentAnswerWriter:  &bytes.Buffer{},
		PromptAnswerWriter:       &bytes.Buffer{},
		Color:                    DarkShellColorScheme,
		SpecialMode:              true,
		SpecialModeType:          specialModeAction,
		SpecialModeGoal:          "show the largest files in this directory",
	}

	state.actionModePrompt("Start now.")

	deadline := time.Now().Add(2 * time.Second)
	for len(llm.streamRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if len(llm.streamRequests) != 1 {
		t.Fatalf("expected one stream request, got %d", len(llm.streamRequests))
	}

	req := llm.streamRequests[0]
	if req.SystemMessage != "action system prompt" {
		t.Fatalf("unexpected system message: %q", req.SystemMessage)
	}
	if len(req.Tools) != 0 {
		t.Fatalf("expected no tools for action mode, got %d", len(req.Tools))
	}
	if len(req.Functions) != 2 {
		t.Fatalf("expected two functions, got %d", len(req.Functions))
	}
	if req.Functions[0].Name != "command" || req.Functions[1].Name != "decline" {
		t.Fatalf("unexpected function list: %#v", req.Functions)
	}
}

func TestActionModePromptIncludesRecentHistory(t *testing.T) {
	llm := &recordingLLM{streamResponse: &util.CompletionResponse{}}
	config := MakeButterfishConfig()
	config.ShellPromptModel = "gpt-5.4"
	config.ShellReasoningEffort = "medium"
	config.ShellMaxHistoryBlockTokens = 1024

	history := NewShellHistory()
	history.Append(historyTypePrompt, "@show the largest file in this dir")
	history.Append(historyTypeShellInput, "find . -maxdepth 1 -type f -exec stat -f '%z %N' {} + | sort -nr | head -n 1")
	history.Append(historyTypeShellOutput, "22023 ./README.md\nExit Code: 0\n")

	state := &ShellState{
		Butterfish: &ButterfishCtx{
			Config:        config,
			PromptLibrary: actionModePromptLibrary{},
			LLMClient:     llm,
		},
		PromptMaxTokens:          defaultShellMaxPromptTokens,
		History:                  history,
		PromptOutputChan:         make(chan *util.CompletionResponse, 1),
		PromptActionAnswerWriter: &bytes.Buffer{},
		PromptAgentAnswerWriter:  &bytes.Buffer{},
		PromptAnswerWriter:       &bytes.Buffer{},
		Color:                    DarkShellColorScheme,
		SpecialMode:              true,
		SpecialModeType:          specialModeAction,
		SpecialModeGoal:          "now the opposite",
	}

	state.actionModePrompt("Start now.")

	deadline := time.Now().Add(2 * time.Second)
	for len(llm.streamRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if len(llm.streamRequests) != 1 {
		t.Fatalf("expected one stream request, got %d", len(llm.streamRequests))
	}

	req := llm.streamRequests[0]
	if len(req.HistoryBlocks) < 3 {
		t.Fatalf("expected action mode request history, got %#v", req.HistoryBlocks)
	}
	historyStr := util.HistoryBlocksToString(req.HistoryBlocks)
	if !strings.Contains(historyStr, "@show the largest file in this dir") {
		t.Fatalf("expected prior action prompt in history, got %s", historyStr)
	}
	if !strings.Contains(historyStr, "22023 ./README.md") {
		t.Fatalf("expected prior shell output in history, got %s", historyStr)
	}
}

func TestActionModeStartDoubleAtEnablesImmediateExecute(t *testing.T) {
	llm := &recordingLLM{streamResponse: &util.CompletionResponse{}}
	config := MakeButterfishConfig()
	config.ShellPromptModel = "gpt-5.4"
	config.ShellReasoningEffort = "medium"
	config.ShellMaxHistoryBlockTokens = 1024

	promptBuf := NewShellBuffer()
	promptBuf.Write("@@show the largest files in this directory")

	state := &ShellState{
		Butterfish: &ButterfishCtx{
			Config:        config,
			PromptLibrary: actionModePromptLibrary{},
			LLMClient:     llm,
		},
		Prompt:                   promptBuf,
		PromptMaxTokens:          defaultShellMaxPromptTokens,
		History:                  NewShellHistory(),
		PromptOutputChan:         make(chan *util.CompletionResponse, 1),
		PromptActionAnswerWriter: &bytes.Buffer{},
		PromptAgentAnswerWriter:  &bytes.Buffer{},
		PromptAnswerWriter:       &bytes.Buffer{},
		Color:                    DarkShellColorScheme,
	}

	state.ActionModeStart()

	if !state.SpecialMode || !state.isActionMode() {
		t.Fatal("expected action mode to be active")
	}
	if !state.SpecialModeUnsafe {
		t.Fatal("expected @@ to enable immediate execution")
	}
	if got := state.SpecialModeGoal; got != "show the largest files in this directory" {
		t.Fatalf("unexpected action mode request: %q", got)
	}
	if len(state.History.Blocks) != 1 {
		t.Fatalf("expected one history block, got %d", len(state.History.Blocks))
	}
	if state.History.Blocks[0].Type != historyTypePrompt {
		t.Fatalf("expected prompt history block, got %#v", state.History.Blocks[0])
	}
	if got := state.History.Blocks[0].Content.String(); got != "@@show the largest files in this directory" {
		t.Fatalf("unexpected stored action prompt: %q", got)
	}
}

func TestActionModeCommandCompleteAppendsShellExitStatus(t *testing.T) {
	state := &ShellState{
		Butterfish:      &ButterfishCtx{Config: &ButterfishConfig{ShellNewlineAutosuggestTimeout: -1}},
		History:         NewShellHistory(),
		SpecialMode:     true,
		SpecialModeType: specialModeAction,
	}

	state.ActionModeCommandComplete(1)

	if state.SpecialMode {
		t.Fatal("expected action mode to be cleared")
	}
	if len(state.History.Blocks) != 1 {
		t.Fatalf("expected one history block, got %d", len(state.History.Blocks))
	}
	block := state.History.Blocks[0]
	if block.Type != historyTypeShellOutput {
		t.Fatalf("expected shell output history, got %d", block.Type)
	}
	if got := block.Content.String(); got != "Exit Code: 1\n" {
		t.Fatalf("unexpected shell output history: %q", got)
	}
}
