package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/PullRequestInc/go-gpt3"
	"github.com/charmbracelet/lipgloss"
	"github.com/joho/godotenv"
)

type GPT struct {
	client gpt3.Client
}

func NewGPT(token string) *GPT {
	client := gpt3.NewClient(token, gpt3.WithDefaultEngine(gpt3.TextDavinci003Engine))

	return &GPT{
		client: client,
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

	err := this.client.CompletionStream(ctx, req, callback)

	return err
}

// Run a GPT completion request and return the response
func (this *GPT) Completion(ctx context.Context, prompt string) (string, error) {
	req := gpt3.CompletionRequest{
		Prompt:    []string{prompt},
		MaxTokens: gpt3.IntPtr(GPTMaxTokens),
	}

	resp, err := this.client.Completion(ctx, req)
	if err != nil {
		return "", err
	}

	return resp.Choices[0].Text, nil
}

func getGPTClient() *GPT {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("You need a .env file in the current directory that defines OPENAI_TOKEN.\ne.g. OPENAI_TOKEN=foobar")
	}

	// initialize GPT API client
	token := os.Getenv("OPENAI_TOKEN")
	return NewGPT(token)
}
