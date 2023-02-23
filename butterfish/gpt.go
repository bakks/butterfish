package butterfish

import (
	"context"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/PullRequestInc/go-gpt3"
	"github.com/bakks/butterfish/util"
)

type GPT struct {
	client        gpt3.Client
	verbose       bool
	verboseWriter io.Writer
}

const gptClientTimeout = 300 * time.Second

func NewGPT(token string, verbose bool, verboseWriter io.Writer) *GPT {
	client := gpt3.NewClient(token,
		gpt3.WithDefaultEngine(gpt3.TextDavinci003Engine),
		gpt3.WithTimeout(gptClientTimeout))

	return &GPT{
		client:        client,
		verbose:       verbose,
		verboseWriter: verboseWriter,
	}
}

func printPrompt(writer io.Writer, prompt string) {
	fmt.Fprintf(writer, "↑ ---\n%s\n-----\n", prompt)
}

func printResponse(writer io.Writer, response string) {
	fmt.Fprintf(writer, "↓ ---\n%s\n-----\n", response)
}

func (this *GPT) CompletionStream(request *util.CompletionRequest, writer io.Writer) (string, error) {
	engine := request.Model
	req := gpt3.CompletionRequest{
		Prompt:      []string{request.Prompt},
		MaxTokens:   &request.MaxTokens,
		Temperature: &request.Temperature,
	}

	strBuilder := strings.Builder{}

	callback := func(resp *gpt3.CompletionResponse) {
		if resp.Choices == nil || len(resp.Choices) == 0 {
			return
		}

		text := resp.Choices[0].Text
		writer.Write([]byte(text))
		strBuilder.WriteString(text)
	}

	if this.verbose {
		printPrompt(this.verboseWriter, request.Prompt)
	}
	err := this.client.CompletionStreamWithEngine(request.Ctx, engine, req, callback)
	fmt.Fprintf(writer, "\n") // GPT doesn't finish with a newline

	return strBuilder.String(), err
}

// Run a GPT completion request and return the response
func (this *GPT) Completion(request *util.CompletionRequest) (string, error) {
	engine := request.Model
	req := gpt3.CompletionRequest{
		Prompt:      []string{request.Prompt},
		MaxTokens:   &request.MaxTokens,
		Temperature: &request.Temperature,
	}

	if this.verbose {
		printPrompt(this.verboseWriter, request.Prompt)
	}
	resp, err := this.client.CompletionWithEngine(request.Ctx, engine, req)
	if err != nil {
		return "", err
	}

	if this.verbose {
		printResponse(this.verboseWriter, resp.Choices[0].Text)
	}
	return resp.Choices[0].Text, nil
}

func (this *GPT) SetVerbose(verbose bool) {
	this.verbose = verbose
}

const GPTEmbeddingsMaxTokens = 8192
const GPTEmbeddingsModel = "text-embedding-ada-002"

func withExponentialBackoff(writer io.Writer, f func() error) error {
	for i := 0; ; i++ {
		err := f()

		if err != nil && strings.Contains(err.Error(), "429:requests") {
			// TODO should probably have a better error detection
			sleepTime := time.Duration(math.Pow(1.6, float64(i+1))) * time.Second
			fmt.Fprintf(writer, "Rate limited, sleeping for %s\n", sleepTime)
			time.Sleep(sleepTime)

			if i > 6 {
				return fmt.Errorf("Getting 429s from GPT-3, giving up after %d retries", i)
			}
			continue
		}
		return err
	}
}

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
			summary += s[:util.Min(20, len(s))]
		}
		summary += "\n]"
		fmt.Fprintf(this.verboseWriter, "%s\n", summary)
	}

	result := [][]float64{}

	err := withExponentialBackoff(this.verboseWriter, func() error {
		resp, err := this.client.Embeddings(ctx, req)
		if err != nil {
			return err
		}

		for _, embedding := range resp.Data {
			result = append(result, embedding.Embedding)
		}
		return nil
	})

	return result, err
}

const GPTEditModel = "code-davinci-edit-001"

func (this *GPT) Edits(ctx context.Context, content, instruction, model string) (string, error) {
	if model == "" {
		model = GPTEditModel
	}

	req := gpt3.EditsRequest{
		Model:       model,
		Input:       content,
		Instruction: instruction,
	}

	if this.verbose {
		printPrompt(this.verboseWriter, fmt.Sprintf("%s\n---\n%s", instruction, content))
	}

	resp, err := this.client.Edits(ctx, req)
	if err != nil {
		return "", err
	}

	return resp.Choices[0].Text, nil
}
