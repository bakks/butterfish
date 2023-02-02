package main

import (
	"context"
	"fmt"
	"io"

	"github.com/PullRequestInc/go-gpt3"
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
func (this *GPT) CompletionStream(ctx context.Context, prompt string, writer io.Writer) error {
	req := gpt3.CompletionRequest{
		Prompt:    []string{prompt},
		MaxTokens: gpt3.IntPtr(GPTMaxTokens),
	}

	callback := func(resp *gpt3.CompletionResponse) {
		if resp.Choices == nil || len(resp.Choices) == 0 {
			return
		}

		text := resp.Choices[0].Text
		writer.Write([]byte(text))
	}

	if this.verbose {
		printPrompt(writer, prompt)
	}
	err := this.client.CompletionStream(ctx, req, callback)
	fmt.Fprintf(writer, "\n") // GPT doesn't finish with a newline

	return err
}

func printPrompt(writer io.Writer, prompt string) {
	fmt.Fprintf(writer, "↑ %s\n", prompt)
}

func printResponse(writer io.Writer, response string) {
	fmt.Fprintf(writer, "↓ %s\n", response)
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

	if this.verbose {
		printResponse(writer, resp.Choices[0].Text)
	}
	return resp.Choices[0].Text, nil
}

func (this *GPT) SetVerbose(verbose bool) {
	this.verbose = verbose
}

const GPTEmbeddingsMaxTokens = 8192
const GPTEmbeddingsModel = "text-embedding-ada-002"

func (this *GPT) Embeddings(ctx context.Context, input []string) ([][]float64, error) {
	req := gpt3.EmbeddingsRequest{
		Input: input,
		Model: GPTEmbeddingsModel,
	}

	if this.verbose {
		summary := fmt.Sprintf("Embedding %d strings: [", len(input))
		for i, s := range input {
			if i > 0 {
				summary += ",\n"
			} else {
				summary += "\n"
			}
			summary += s
		}
		summary += "]"
	}

	resp, err := this.client.Embeddings(ctx, req)
	if err != nil {
		return nil, err
	}

	result := [][]float64{}
	for _, embedding := range resp.Data {
		result = append(result, embedding.Embedding)
	}

	return result, nil
}
