package butterfish

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bakks/butterfish/util"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

func TestBuildInputItemsUsesFunctionNameFallbackForFunctionOutput(t *testing.T) {
	req := &util.CompletionRequest{
		Model: "gpt-5.5",
		HistoryBlocks: []util.HistoryBlock{
			{
				Type:           historyTypeLLMOutput,
				FunctionName:   "command",
				FunctionParams: `{"cmd":"ls -la"}`,
			},
			{
				Type:         historyTypeFunctionOutput,
				FunctionName: "command",
				Content:      "Command staged in the shell for user review.",
			},
		},
	}

	items := buildInputItems(req)
	if len(items) != 2 {
		t.Fatalf("expected two input items, got %d", len(items))
	}
	if items[0].OfFunctionCall == nil {
		t.Fatalf("expected first item to be function call, got %#v", items[0])
	}
	if got := items[0].OfFunctionCall.CallID; got != "command" {
		t.Fatalf("expected function call id fallback 'command', got %q", got)
	}
	if items[1].OfFunctionCallOutput == nil {
		t.Fatalf("expected second item to be function_call_output, got %#v", items[1])
	}
	if got := items[1].OfFunctionCallOutput.CallID; got != "command" {
		t.Fatalf("expected function output call id fallback 'command', got %q", got)
	}
}

func TestBuildInputItemsTruncatesShellCallOutput(t *testing.T) {
	req := &util.CompletionRequest{
		Model: "gpt-5.5",
		HistoryBlocks: []util.HistoryBlock{
			{
				Type: historyTypeLLMOutput,
				ShellCall: &util.ShellCall{
					CallID:          "call_1",
					Commands:        []string{"python noisy.py"},
					MaxOutputLength: 256,
				},
			},
			{
				Type:     historyTypeToolOutput,
				ToolType: "shell",
				ShellCallOutput: &util.ShellCallOutput{
					CallID:          "call_1",
					MaxOutputLength: 256,
					Output: []util.ShellCallOutputItem{
						{
							Stdout: strings.Repeat("a", 1024) + "important tail",
							Outcome: util.ShellCallOutcome{
								Type:     "exit",
								ExitCode: 0,
							},
						},
					},
				},
			},
		},
	}

	items := buildInputItems(req)
	if len(items) != 2 {
		t.Fatalf("expected two input items, got %d", len(items))
	}
	output := items[1].OfShellCallOutput
	if output == nil {
		t.Fatalf("expected shell_call_output, got %#v", items[1])
	}
	stdout := output.Output[0].Stdout
	if len(stdout) > 256 {
		t.Fatalf("expected stdout to be capped at 256 bytes, got %d", len(stdout))
	}
	if !strings.Contains(stdout, "butterfish truncated") {
		t.Fatalf("expected truncation marker, got %q", stdout)
	}
	if !strings.HasSuffix(stdout, "important tail") {
		t.Fatalf("expected output tail to be preserved, got %q", stdout)
	}
}

func TestRecordShellCallMergePrefersNonEmpty(t *testing.T) {
	callMap := map[string]*util.ShellCall{}
	order := []string{}

	recordShellCall(callMap, &order, &util.ShellCall{CallID: "call_1"})
	recordShellCall(callMap, &order, &util.ShellCall{CallID: "call_1", Commands: []string{"ls -l"}})

	if len(order) != 1 || order[0] != "call_1" {
		t.Fatalf("unexpected order: %v", order)
	}
	call := callMap["call_1"]
	if call == nil || len(call.Commands) != 1 || call.Commands[0] != "ls -l" {
		t.Fatalf("expected commands to be set, got: %#v", call)
	}
}

func TestRecordShellCallDoesNotOverwriteWithEmpty(t *testing.T) {
	callMap := map[string]*util.ShellCall{}
	order := []string{}

	recordShellCall(callMap, &order, &util.ShellCall{CallID: "call_1", Commands: []string{"pwd"}})
	recordShellCall(callMap, &order, &util.ShellCall{CallID: "call_1"})

	call := callMap["call_1"]
	if call == nil || len(call.Commands) != 1 || call.Commands[0] != "pwd" {
		t.Fatalf("expected commands preserved, got: %#v", call)
	}
}

func TestMergeShellCallsFromOutput(t *testing.T) {
	var item responses.ResponseOutputItemUnion
	payload := []byte(`{"type":"shell_call","id":"item_1","call_id":"call_1","action":{"commands":["ls -l"],"timeout_ms":120000,"max_output_length":4096},"status":"in_progress"}`)
	if err := json.Unmarshal(payload, &item); err != nil {
		t.Fatalf("unmarshal shell_call: %v", err)
	}

	callMap := map[string]*util.ShellCall{}
	order := []string{}
	mergeShellCallsFromOutput(callMap, &order, []responses.ResponseOutputItemUnion{item})

	if len(order) != 1 || order[0] != "call_1" {
		t.Fatalf("unexpected order: %v", order)
	}
	call := callMap["call_1"]
	if call == nil || len(call.Commands) != 1 || call.Commands[0] != "ls -l" {
		t.Fatalf("expected merged shell call, got: %#v", call)
	}
}

func TestBuildResponseParamsIncludesReasoningEffort(t *testing.T) {
	req := &util.CompletionRequest{
		Model:           "gpt-5.5",
		Prompt:          "hi",
		MaxTokens:       64,
		ReasoningEffort: "medium",
	}

	params := buildResponseParams(req, req.ReasoningEffort)
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	if !bytes.Contains(raw, []byte(`"reasoning":{"effort":"medium"`)) {
		t.Fatalf("expected reasoning effort in params json, got: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"summary":"concise"`)) {
		t.Fatalf("expected reasoning summary in params json, got: %s", raw)
	}
	if bytes.Contains(raw, []byte(`"temperature"`)) {
		t.Fatalf("did not expect temperature in params json, got: %s", raw)
	}
}

func TestReasoningSummaryPrinterWritesOwnLine(t *testing.T) {
	buf := &bytes.Buffer{}
	printer := newReasoningSummaryPrinter(buf)

	printer.WriteDelta("looking")
	printer.WriteDelta(" around")
	printer.FinishSummary()
	printer.BeforeOutput()
	buf.WriteString("answer")

	if got, want := buf.String(), "\nReasoning: looking around\nanswer"; got != want {
		t.Fatalf("unexpected reasoning summary output: got %q want %q", got, want)
	}
}

func TestReasoningSummaryPrinterClosesLineBeforeOutput(t *testing.T) {
	buf := &bytes.Buffer{}
	printer := newReasoningSummaryPrinter(buf)

	printer.WriteDelta("checking")
	printer.BeforeOutput()
	buf.WriteString("answer")

	if got, want := buf.String(), "\nReasoning: checking\nanswer"; got != want {
		t.Fatalf("unexpected reasoning summary output: got %q want %q", got, want)
	}
}

func TestBuildResponseParamsOmitsMaxOutputTokensWhenUnset(t *testing.T) {
	req := &util.CompletionRequest{
		Model:  "gpt-5.5",
		Prompt: "hi",
	}

	params := buildResponseParams(req, "")
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	if bytes.Contains(raw, []byte(`"max_output_tokens"`)) {
		t.Fatalf("did not expect max_output_tokens in params json, got: %s", raw)
	}
}

func TestBuildResponseParamsIncludesMaxOutputTokensWhenSet(t *testing.T) {
	req := &util.CompletionRequest{
		Model:     "gpt-5.5",
		Prompt:    "hi",
		MaxTokens: 64,
	}

	params := buildResponseParams(req, "")
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	if !bytes.Contains(raw, []byte(`"max_output_tokens":64`)) {
		t.Fatalf("expected max_output_tokens in params json, got: %s", raw)
	}
}

func TestBuildResponseParamsIncludesServiceTier(t *testing.T) {
	req := &util.CompletionRequest{
		Model:       "gpt-5.5",
		Prompt:      "hi",
		MaxTokens:   64,
		ServiceTier: "fast",
	}

	params := buildResponseParams(req, "")
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	if !bytes.Contains(raw, []byte(`"service_tier":"priority"`)) {
		t.Fatalf("expected fast service tier to map to priority, got: %s", raw)
	}
}

func TestBuildResponseParamsOmitsReasoningWhenUnset(t *testing.T) {
	req := &util.CompletionRequest{
		Model:     "gpt-4.1",
		Prompt:    "hi",
		MaxTokens: 64,
	}

	params := buildResponseParams(req, "")
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	if bytes.Contains(raw, []byte(`"reasoning"`)) {
		t.Fatalf("did not expect reasoning in params json, got: %s", raw)
	}
}

func TestResponseIncompleteErrorForMaxOutputTokens(t *testing.T) {
	resp := &responses.Response{
		Status: responses.ResponseStatusIncomplete,
		IncompleteDetails: responses.ResponseIncompleteDetails{
			Reason: "max_output_tokens",
		},
		MaxOutputTokens: 64,
	}

	err := responseIncompleteError(resp)
	if err == nil {
		t.Fatal("expected max output token error")
	}
	if !strings.Contains(err.Error(), "max_output_tokens (64)") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResponseIncompleteErrorIgnoresOtherReasons(t *testing.T) {
	resp := &responses.Response{
		Status: responses.ResponseStatusIncomplete,
		IncompleteDetails: responses.ResponseIncompleteDetails{
			Reason: "content_filter",
		},
	}

	if err := responseIncompleteError(resp); err != nil {
		t.Fatalf("did not expect content filter to be reported as token limit: %v", err)
	}
}

func TestIsReasoningUnsupportedError(t *testing.T) {
	apiErr := &openai.Error{
		StatusCode: 400,
		Param:      "reasoning.effort",
		Message:    "reasoning is not supported for this model",
	}
	if !isReasoningUnsupportedError(apiErr) {
		t.Fatalf("expected reasoning unsupported error to be detected")
	}
}

func TestIsReasoningUnsupportedErrorFalseForUnrelatedErrors(t *testing.T) {
	apiErr := &openai.Error{
		StatusCode: 400,
		Param:      "max_output_tokens",
		Message:    "max_output_tokens is invalid",
	}
	if isReasoningUnsupportedError(apiErr) {
		t.Fatalf("did not expect unrelated error to be detected as reasoning unsupported")
	}
}

func TestIsServiceTierUnsupportedError(t *testing.T) {
	apiErr := &openai.Error{
		StatusCode: 400,
		Param:      "service_tier",
		Message:    "service_tier is not supported",
	}
	if !isServiceTierUnsupportedError(apiErr) {
		t.Fatalf("expected service_tier unsupported error to be detected")
	}
}
