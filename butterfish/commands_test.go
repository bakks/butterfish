package butterfish

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/bakks/butterfish/util"
)

type recordingLLM struct {
	completionRequests []*util.CompletionRequest
	streamRequests     []*util.CompletionRequest
	completionResponse *util.CompletionResponse
	streamResponse     *util.CompletionResponse
}

func (r *recordingLLM) CompletionStream(request *util.CompletionRequest, writer io.Writer) (*util.CompletionResponse, error) {
	r.streamRequests = append(r.streamRequests, request)
	if r.streamResponse != nil {
		return r.streamResponse, nil
	}
	return &util.CompletionResponse{}, nil
}

func (r *recordingLLM) Completion(request *util.CompletionRequest) (*util.CompletionResponse, error) {
	r.completionRequests = append(r.completionRequests, request)
	if r.completionResponse != nil {
		return r.completionResponse, nil
	}
	return &util.CompletionResponse{}, nil
}

type stubPromptLibrary struct{}

func (stubPromptLibrary) GetPrompt(name string, args ...string) (string, error) {
	switch name {
	case "generate_command":
		return "generate command prompt", nil
	case "fix_command":
		return "fix command prompt", nil
	default:
		return "system prompt", nil
	}
}

func (stubPromptLibrary) GetUninterpolatedPrompt(name string) (string, error) {
	return "", errors.New("not implemented")
}

func (stubPromptLibrary) InterpolatePrompt(prompt string, args ...string) (string, error) {
	return prompt, nil
}

func newTestButterfishCtx(llm LLM) *ButterfishCtx {
	config := MakeButterfishConfig()
	return &ButterfishCtx{
		Ctx:           context.Background(),
		Out:           &bytes.Buffer{},
		Config:        config,
		PromptLibrary: stubPromptLibrary{},
		LLMClient:     llm,
	}
}

func TestParseCommandReasoningEffortDefaults(t *testing.T) {
	ctx := &ButterfishCtx{}

	promptParsed, promptOptions, err := ctx.ParseCommand("prompt hello")
	if err != nil {
		t.Fatalf("parse prompt: %v", err)
	}
	if promptParsed.Command() != "prompt <prompt>" {
		t.Fatalf("unexpected prompt command: %s", promptParsed.Command())
	}
	if promptOptions.Prompt.ReasoningEffort != "medium" {
		t.Fatalf("expected prompt reasoning effort to default to medium, got %q", promptOptions.Prompt.ReasoningEffort)
	}

	gencmdParsed, gencmdOptions, err := ctx.ParseCommand("gencmd list files")
	if err != nil {
		t.Fatalf("parse gencmd: %v", err)
	}
	if gencmdParsed.Command() != "gencmd <prompt>" {
		t.Fatalf("unexpected gencmd command: %s", gencmdParsed.Command())
	}
	if gencmdOptions.Gencmd.ReasoningEffort != "medium" {
		t.Fatalf("expected gencmd reasoning effort to default to medium, got %q", gencmdOptions.Gencmd.ReasoningEffort)
	}

	execParsed, execOptions, err := ctx.ParseCommand("exec ls")
	if err != nil {
		t.Fatalf("parse exec: %v", err)
	}
	if execParsed.Command() != "exec <command>" {
		t.Fatalf("unexpected exec command: %s", execParsed.Command())
	}
	if execOptions.Exec.ReasoningEffort != "medium" {
		t.Fatalf("expected exec reasoning effort to default to medium, got %q", execOptions.Exec.ReasoningEffort)
	}
}

func TestParseCommandReasoningEffortOverrides(t *testing.T) {
	ctx := &ButterfishCtx{}

	_, promptOptions, err := ctx.ParseCommand("prompt -r low hello")
	if err != nil {
		t.Fatalf("parse prompt override: %v", err)
	}
	if promptOptions.Prompt.ReasoningEffort != "low" {
		t.Fatalf("expected prompt reasoning effort override, got %q", promptOptions.Prompt.ReasoningEffort)
	}

	_, gencmdOptions, err := ctx.ParseCommand("gencmd -r high list files")
	if err != nil {
		t.Fatalf("parse gencmd override: %v", err)
	}
	if gencmdOptions.Gencmd.ReasoningEffort != "high" {
		t.Fatalf("expected gencmd reasoning effort override, got %q", gencmdOptions.Gencmd.ReasoningEffort)
	}

	_, execOptions, err := ctx.ParseCommand("exec -r low ls")
	if err != nil {
		t.Fatalf("parse exec override: %v", err)
	}
	if execOptions.Exec.ReasoningEffort != "low" {
		t.Fatalf("expected exec reasoning effort override, got %q", execOptions.Exec.ReasoningEffort)
	}
}

func TestPromptIncludesReasoningEffort(t *testing.T) {
	llm := &recordingLLM{}
	ctx := newTestButterfishCtx(llm)

	_, err := ctx.Prompt(&promptCommand{
		Prompt:          "hello",
		SysMsg:          "system prompt",
		Model:           "gpt-5.4",
		ReasoningEffort: "low",
		NumTokens:       64,
		NoColor:         true,
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}

	if len(llm.streamRequests) != 1 {
		t.Fatalf("expected one streaming request, got %d", len(llm.streamRequests))
	}
	if llm.streamRequests[0].ReasoningEffort != "low" {
		t.Fatalf("expected prompt reasoning effort low, got %q", llm.streamRequests[0].ReasoningEffort)
	}
}

func TestGencmdIncludesReasoningEffort(t *testing.T) {
	llm := &recordingLLM{
		completionResponse: &util.CompletionResponse{Completion: "ls"},
	}
	ctx := newTestButterfishCtx(llm)

	_, err := ctx.gencmdCommand("list files", "high")
	if err != nil {
		t.Fatalf("gencmd: %v", err)
	}

	if len(llm.completionRequests) != 1 {
		t.Fatalf("expected one completion request, got %d", len(llm.completionRequests))
	}
	if llm.completionRequests[0].ReasoningEffort != "high" {
		t.Fatalf("expected gencmd reasoning effort high, got %q", llm.completionRequests[0].ReasoningEffort)
	}
}

func TestExecIncludesReasoningEffort(t *testing.T) {
	llm := &recordingLLM{
		streamResponse: &util.CompletionResponse{Completion: "not a shell command"},
	}
	ctx := newTestButterfishCtx(llm)

	err := ctx.execAndCheck(context.Background(), "false", "low")
	if err == nil {
		t.Fatal("expected execAndCheck to fail when command parse fails")
	}
	if !strings.Contains(err.Error(), "Could not find command") {
		t.Fatalf("expected command parse failure, got %v", err)
	}

	if len(llm.streamRequests) != 1 {
		t.Fatalf("expected one streaming request, got %d", len(llm.streamRequests))
	}
	if llm.streamRequests[0].ReasoningEffort != "low" {
		t.Fatalf("expected exec reasoning effort low, got %q", llm.streamRequests[0].ReasoningEffort)
	}
}
