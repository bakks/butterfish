package butterfish

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/bakks/butterfish/util"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

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
		Model:           "gpt-5.4",
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
	if bytes.Contains(raw, []byte(`"temperature"`)) {
		t.Fatalf("did not expect temperature in params json, got: %s", raw)
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
