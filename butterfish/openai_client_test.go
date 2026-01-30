package butterfish

import (
	"encoding/json"
	"testing"

	"github.com/bakks/butterfish/util"
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
