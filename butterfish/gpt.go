package butterfish

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"strings"
	"time"

	"github.com/PullRequestInc/go-gpt3"
	"github.com/bakks/butterfish/util"
)

const ERR_429 = "429:insufficient_quota"
const ERR_429_HELP = "You are likely using a free OpenAI account without a subscription activated, this error means you are out of credits. To resolve it, set up a subscription at https://platform.openai.com/account/billing/overview. This requires a credit card and payment, run `butterfish help` for guidance on managing cost. Once you have a subscription set up you must issue a NEW OpenAI token, your previous token will not reflect the subscription."

var LegacyModelTypes = []string{
	gpt3.TextAda001Engine,
	gpt3.TextBabbage001Engine,
	gpt3.TextCurie001Engine,
	gpt3.TextDavinci001Engine,
	gpt3.TextDavinci002Engine,
	gpt3.TextDavinci003Engine,
}

func IsLegacyModel(model string) bool {
	for _, legacyModel := range LegacyModelTypes {
		if model == legacyModel {
			return true
		}
	}
	return false
}

type GPT struct {
	client        gpt3.Client
	verbose       bool
	useLogging    bool
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

func (this *GPT) SetVerbose(verbose bool) {
	this.verbose = verbose
}

func (this *GPT) SetVerboseLogging() {
	this.useLogging = true
	this.verbose = true
}

func (this *GPT) printPrompt(prompt string) {
	this.Printf("↑ ---\n%s\n-----\n", prompt)
}

func (this *GPT) printResponse(response string) {
	this.Printf("↓ ---\n%s\n-----\n", response)
}

func (this *GPT) Printf(format string, args ...any) {
	if !this.verbose {
		return
	}

	if this.useLogging {
		log.Printf(format, args...)
	} else {
		fmt.Fprintf(this.verboseWriter, format, args...)
	}
}

func ChatCompletionRequestMessagesString(msgs []gpt3.ChatCompletionRequestMessage) string {
	out := []string{}
	for _, msg := range msgs {
		line := fmt.Sprintf("%s:  %s", msg.Role, msg.Content)
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func ShellHistoryBlockToGPTChat(systemMsg string, blocks []util.HistoryBlock) []gpt3.ChatCompletionRequestMessage {
	out := []gpt3.ChatCompletionRequestMessage{
		{
			Role:    "system",
			Content: systemMsg,
		},
	}

	for _, block := range blocks {
		role := "user"
		if block.Type == historyTypeLLMOutput {
			role = "assistant"
		}

		nextBlock := gpt3.ChatCompletionRequestMessage{
			Role:    role,
			Content: block.Content,
		}
		out = append(out, nextBlock)
	}

	return out
}

// We're doing completions through the chat API by default, this routes
// to the legacy completion API if the model is the legacy model.
func (this *GPT) Completion(request *util.CompletionRequest) (string, error) {
	var result string
	var err error

	if IsLegacyModel(request.Model) {
		result, err = this.LegacyCompletion(request)
	} else if request.HistoryBlocks == nil {
		result, err = this.SimpleChatCompletion(request)
	} else {
		result, err = this.FullChatCompletion(request)
	}

	// This error means the user needs to set up a subscription, give advice
	if err != nil && strings.Contains(err.Error(), ERR_429) {
		err = fmt.Errorf("%s\n\n%s", err.Error(), ERR_429_HELP)
	}

	return result, err
}

// We're doing completions through the chat API by default, this routes
// to the legacy completion API if the model is the legacy model.
func (this *GPT) CompletionStream(request *util.CompletionRequest, writer io.Writer) (string, error) {
	var result string
	var err error

	if IsLegacyModel(request.Model) {
		result, err = this.LegacyCompletionStream(request, writer)
	} else if request.HistoryBlocks == nil {
		result, err = this.SimpleChatCompletionStream(request, writer)
	} else {
		result, err = this.FullChatCompletionStream(request, writer)
	}

	// This error means the user needs to set up a subscription, give advice
	if err != nil && strings.Contains(err.Error(), ERR_429) {
		err = fmt.Errorf("%s\n\n%s", err.Error(), ERR_429_HELP)
	}

	return result, err
}

func (this *GPT) LegacyCompletionStream(request *util.CompletionRequest, writer io.Writer) (string, error) {
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

	this.printPrompt(request.Prompt)
	err := this.client.CompletionStreamWithEngine(request.Ctx, engine, req, callback)
	fmt.Fprintf(writer, "\n") // GPT doesn't finish with a newline

	return strBuilder.String(), err
}

func (this *GPT) SimpleChatCompletionStream(request *util.CompletionRequest, writer io.Writer) (string, error) {
	req := gpt3.ChatCompletionRequest{
		Model: request.Model,
		Messages: []gpt3.ChatCompletionRequestMessage{
			{
				Role:    "system",
				Content: request.SystemMessage,
			},
			{
				Role:    "user",
				Content: request.Prompt,
			},
		},
		MaxTokens:   request.MaxTokens,
		Temperature: &request.Temperature,
		N:           1,
	}

	return this.doChatStreamCompletion(request.Ctx, req, writer)
}

func (this *GPT) FullChatCompletionStream(request *util.CompletionRequest, writer io.Writer) (string, error) {
	if request.SystemMessage == "" {
		return "", errors.New("system message required for full chat completion")
	}
	gptHistory := ShellHistoryBlockToGPTChat(request.SystemMessage, request.HistoryBlocks)
	messages := append(gptHistory, gpt3.ChatCompletionRequestMessage{
		Role:    "user",
		Content: request.Prompt,
	})

	req := gpt3.ChatCompletionRequest{
		Model:       request.Model,
		Messages:    messages,
		MaxTokens:   request.MaxTokens,
		Temperature: &request.Temperature,
		N:           1,
	}

	return this.doChatStreamCompletion(request.Ctx, req, writer)
}

func (this *GPT) doChatStreamCompletion(ctx context.Context, req gpt3.ChatCompletionRequest, writer io.Writer) (string, error) {

	strBuilder := strings.Builder{}

	callback := func(resp *gpt3.ChatCompletionStreamResponse) {
		if resp.Choices == nil || len(resp.Choices) == 0 {
			return
		}

		text := resp.Choices[0].Delta.Content
		if text == "" {
			return
		}

		writer.Write([]byte(text))
		strBuilder.WriteString(text)
	}

	this.printPrompt(ChatCompletionRequestMessagesString(req.Messages))
	err := this.client.ChatCompletionStream(ctx, req, callback)
	fmt.Fprintf(writer, "\n") // GPT doesn't finish with a newline

	return strBuilder.String(), err
}

// Run a GPT completion request and return the response
func (this *GPT) LegacyCompletion(request *util.CompletionRequest) (string, error) {
	engine := request.Model
	req := gpt3.CompletionRequest{
		Prompt:      []string{request.Prompt},
		MaxTokens:   &request.MaxTokens,
		Temperature: &request.Temperature,
	}

	this.printPrompt(request.Prompt)
	resp, err := this.client.CompletionWithEngine(request.Ctx, engine, req)
	if err != nil {
		return "", err
	}

	text := resp.Choices[0].Text
	// clean whitespace prefix and suffix from text
	text = strings.TrimSpace(text)

	this.printResponse(text)
	return text, nil
}

func (this *GPT) FullChatCompletion(request *util.CompletionRequest) (string, error) {
	if request.SystemMessage == "" {
		return "", errors.New("system message is required for full chat completion")
	}

	gptHistory := ShellHistoryBlockToGPTChat(request.SystemMessage, request.HistoryBlocks)
	messages := append(gptHistory, gpt3.ChatCompletionRequestMessage{
		Role:    "user",
		Content: request.Prompt,
	})

	req := gpt3.ChatCompletionRequest{
		Model:       request.Model,
		Messages:    messages,
		MaxTokens:   request.MaxTokens,
		Temperature: &request.Temperature,
		N:           1,
	}

	return this.doChatCompletion(request.Ctx, req)
}

func (this *GPT) SimpleChatCompletion(request *util.CompletionRequest) (string, error) {
	req := gpt3.ChatCompletionRequest{
		Model: request.Model,
		Messages: []gpt3.ChatCompletionRequestMessage{
			{
				Role:    "system",
				Content: request.SystemMessage,
			},
			{
				Role:    "user",
				Content: request.Prompt,
			},
		},
		MaxTokens:   request.MaxTokens,
		Temperature: &request.Temperature,
		N:           1,
	}

	return this.doChatCompletion(request.Ctx, req)
}

func (this *GPT) doChatCompletion(ctx context.Context, request gpt3.ChatCompletionRequest) (string, error) {
	this.printPrompt(ChatCompletionRequestMessagesString(request.Messages))
	resp, err := this.client.ChatCompletion(ctx, request)
	if err != nil {
		return "", err
	}

	responseText := resp.Choices[0].Message.Content

	this.printResponse(responseText)
	return responseText, nil
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
		this.Printf("%s\n", summary)
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

func (this *GPT) Edits(ctx context.Context,
	content, instruction, model string,
	temperature float32) (string, error) {
	if model == "" {
		model = GPTEditModel
	}

	req := gpt3.EditsRequest{
		Model:       model,
		Input:       content,
		Instruction: instruction,
		Temperature: &temperature,
	}

	this.printPrompt(fmt.Sprintf("%s\n---\n%s", instruction, content))

	resp, err := this.client.Edits(ctx, req)
	if err != nil {
		return "", err
	}

	return resp.Choices[0].Text, nil
}
