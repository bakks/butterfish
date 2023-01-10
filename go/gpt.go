package main

import (
	"context"
	"fmt"
	"io"

	"github.com/PullRequestInc/go-gpt3"
	"github.com/charmbracelet/lipgloss"
)

type GPT struct {
	client  gpt3.Client
	verbose bool
}

func NewGPT(token string, verbose bool) *GPT {
	client := gpt3.NewClient(token, gpt3.WithDefaultEngine(gpt3.TextDavinci003Engine))

	return &GPT{
		client:  client,
		verbose: verbose,
	}
}

// Run a GPT completion request and stream the response to the given writer
// TODO the CompletionStream gpt3 method doesn't currently pass any signal
// that it's done streaming results.
func (this *GPT) CompletionStream(ctx context.Context, prompt string, writer io.Writer, style lipgloss.Style) error {
	req := gpt3.CompletionRequest{
		Prompt:    []string{prompt},
		MaxTokens: gpt3.IntPtr(GPTMaxTokens),
	}

	callback := func(resp *gpt3.CompletionResponse) {
		if resp.Choices == nil || len(resp.Choices) == 0 {
			return
		}

		text := resp.Choices[0].Text
		if text != "NOOP" {
			rendered := style.Render(text)
			fmt.Fprintf(writer, rendered)
		}
	}

	if this.verbose {
		printPrompt(writer, prompt)
	}
	err := this.client.CompletionStream(ctx, req, callback)

	return err
}

func printPrompt(writer io.Writer, prompt string) {
	fmt.Fprintf(writer, "↑ %s", prompt)
}

// Run a GPT completion request and return the response
func (this *GPT) Completion(ctx context.Context, prompt string, writer io.Writer) (string, error) {
	req := gpt3.CompletionRequest{
		Prompt:    []string{prompt},
		MaxTokens: gpt3.IntPtr(GPTMaxTokens),
	}

	if this.verbose {
		printPrompt(writer, prompt)
	}
	resp, err := this.client.Completion(ctx, req)
	if err != nil {
		return "", err
	}

	return resp.Choices[0].Text, nil
}

func (this *GPT) SetVerbose(verbose bool) {
	this.verbose = verbose
}
