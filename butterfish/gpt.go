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

	"github.com/bakks/butterfish/util"
	openai "github.com/sashabaranov/go-openai"
)

const ERR_429 = "429:insufficient_quota"
const ERR_429_HELP = "You are likely using a free OpenAI account without a subscription activated, this error means you are out of credits. To resolve it, set up a subscription at https://platform.openai.com/account/billing/overview. This requires a credit card and payment, run `butterfish help` for guidance on managing cost. Once you have a subscription set up you must issue a NEW OpenAI token, your previous token will not reflect the subscription."

var LegacyModelTypes = []string{
	openai.GPT3TextAda001,
	openai.GPT3TextBabbage001,
	openai.GPT3TextCurie001,
	openai.GPT3TextDavinci001,
	openai.GPT3TextDavinci002,
	openai.GPT3TextDavinci003,
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
	client  *openai.Client
	verbose bool
}

const gptClientTimeout = 300 * time.Second

func NewGPT(token string, verbose bool) *GPT {
	client := openai.NewClient(token)

	return &GPT{
		client:  client,
		verbose: verbose,
	}
}

func (this *GPT) SetVerbose(verbose bool) {
	this.verbose = verbose
}

func (this *GPT) printPrompt(prompt string) {
	this.Printf("↑ ---\n%s\n-----\n", prompt)
}

func (this *GPT) logFullRequest(req openai.ChatCompletionRequest) {
	if !this.verbose {
		return
	}
	LogCompletionRequest(req)
}

func (this *GPT) logFullResponse(resp util.CompletionResponse) {
	if !this.verbose {
		return
	}
	LogCompletionResponse(resp)
}

func (this *GPT) printResponse(response string) {
	this.Printf("↓ ---\n%s\n-----\n", response)
}

func LogCompletionResponse(resp util.CompletionResponse) {
	box := LoggingBox{
		Title:   " Completion Response /v1/chat/completions ",
		Content: resp.Completion,
		Color:   0,
	}

	if resp.FunctionName != "" {
		box.Children = []LoggingBox{
			{
				Title:   "Function Call",
				Content: fmt.Sprintf("%s\n%s", resp.FunctionName, resp.FunctionParameters),
				Color:   2,
			},
		}
	}

	PrintLoggingBox(box)
}

func LogCompletionRequest(req openai.ChatCompletionRequest) {
	meta := fmt.Sprintf("model:       %s\ntemperature: %f\nmax_tokens:  %d",
		req.Model, req.Temperature, req.MaxTokens)

	historyBoxes := []LoggingBox{}
	for _, history := range req.Messages {
		var color int

		switch history.Role {
		case "user":
			color = 4
		case "assistant":
			color = 5
		case "system":
			color = 6
		case "function":
			color = 3
		}

		historyBoxes = append(historyBoxes, LoggingBox{
			Title:   history.Role,
			Content: history.Content,
			Color:   color,
		})
	}

	box := LoggingBox{
		Title:   " Completion Request /v1/chat/completions ",
		Content: meta,
		Color:   0,
		Children: []LoggingBox{
			{
				Title:    "Messages",
				Children: historyBoxes,
				Color:    1,
			},
		},
	}

	functionBoxes := []LoggingBox{}

	for _, function := range req.Functions {
		// list function parameters in a string
		functionBoxes = append(functionBoxes, LoggingBox{
			Title:   function.Name,
			Content: fmt.Sprintf("%s\n%s", function.Description, function.Parameters),
			Color:   3,
		})
	}

	if len(functionBoxes) > 0 {
		box.Children = append(box.Children, LoggingBox{
			Title:    "Functions",
			Children: functionBoxes,
			Color:    2,
		})
	}

	PrintLoggingBox(box)
}

func ChatCompletionRequestMessagesString(msgs []openai.ChatCompletionMessage) string {
	out := []string{}
	for _, msg := range msgs {
		line := fmt.Sprintf("%s:  %s", msg.Role, msg.Content)
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func ShellHistoryBlockToGPTChat(systemMsg string, blocks []util.HistoryBlock) []openai.ChatCompletionMessage {
	out := []openai.ChatCompletionMessage{
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

		nextBlock := openai.ChatCompletionMessage{
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
func (this *GPT) CompletionStream(request *util.CompletionRequest, writer io.Writer) (*util.CompletionResponse, error) {
	var result *util.CompletionResponse
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

func (this *GPT) LegacyCompletionStream(request *util.CompletionRequest, writer io.Writer) (*util.CompletionResponse, error) {
	req := openai.CompletionRequest{
		Prompt:      []string{request.Prompt},
		Model:       request.Model,
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
	}

	strBuilder := strings.Builder{}

	callback := func(resp openai.CompletionResponse) {
		if resp.Choices == nil || len(resp.Choices) == 0 {
			return
		}

		text := resp.Choices[0].Text
		writer.Write([]byte(text))
		strBuilder.WriteString(text)
	}

	this.printPrompt(request.Prompt)
	stream, err := this.client.CreateCompletionStream(request.Ctx, req)

	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, err
		}

		callback(response)
	}
	fmt.Fprintf(writer, "\n") // GPT doesn't finish with a newline

	response := util.CompletionResponse{
		Completion: strBuilder.String(),
	}

	return &response, err
}

func (this *GPT) SimpleChatCompletionStream(request *util.CompletionRequest, writer io.Writer) (*util.CompletionResponse, error) {
	req := openai.ChatCompletionRequest{
		Model: request.Model,
		Messages: []openai.ChatCompletionMessage{
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
		Temperature: request.Temperature,
		N:           1,
	}

	return this.doChatStreamCompletion(request.Ctx, req, writer)
}

func convertToOpenaiFunctions(funcs []util.FunctionDefinition) []openai.FunctionDefinition {
	out := []openai.FunctionDefinition{}
	for _, f := range funcs {
		out = append(out, openai.FunctionDefinition{
			Name:        f.Name,
			Description: f.Description,
			Parameters:  f.Parameters,
		})
	}
	return out
}

func (this *GPT) FullChatCompletionStream(request *util.CompletionRequest, writer io.Writer) (*util.CompletionResponse, error) {
	if request.SystemMessage == "" {
		return nil, errors.New("system message required for full chat completion")
	}
	gptHistory := ShellHistoryBlockToGPTChat(request.SystemMessage, request.HistoryBlocks)
	if request.Prompt != "" {
		gptHistory = append(gptHistory, openai.ChatCompletionMessage{
			Role:    "user",
			Content: request.Prompt,
		})
	}

	req := openai.ChatCompletionRequest{
		Model:       request.Model,
		Messages:    gptHistory,
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		N:           1,
		Functions:   convertToOpenaiFunctions(request.Functions),
	}

	return this.doChatStreamCompletion(request.Ctx, req, writer)
}

func (this *GPT) doChatStreamCompletion(ctx context.Context, req openai.ChatCompletionRequest, printWriter io.Writer) (*util.CompletionResponse, error) {

	var responseContent strings.Builder
	var functionName string
	var functionArgs strings.Builder

	callback := func(resp openai.ChatCompletionStreamResponse) {
		if resp.Choices == nil || len(resp.Choices) == 0 {
			return
		}

		text := resp.Choices[0].Delta.Content
		functionCall := resp.Choices[0].Delta.FunctionCall

		// When a function is streaming back we appear to get the function name
		// always as one string (even if very long) followed by small chunks
		// of tokens for the arguments
		if functionCall != nil {
			if functionCall.Name != "" {
				functionName = functionCall.Name
				printWriter.Write([]byte(functionName))
				printWriter.Write([]byte("("))
			}
			if functionCall.Arguments != "" {
				functionArgs.WriteString(functionCall.Arguments)
				printWriter.Write([]byte(functionCall.Arguments))
			}
		}

		if text == "" {
			return
		}

		printWriter.Write([]byte(text))
		responseContent.WriteString(text)
	}

	this.logFullRequest(req)
	stream, err := this.client.CreateChatCompletionStream(ctx, req)

	if err != nil {
		return nil, err
	}

	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, err
		}

		callback(response)
	}

	if functionName != "" {
		printWriter.Write([]byte(")"))
	}

	fmt.Fprintf(printWriter, "\n") // GPT doesn't finish with a newline

	response := util.CompletionResponse{
		Completion:         responseContent.String(),
		FunctionName:       functionName,
		FunctionParameters: functionArgs.String(),
	}
	this.logFullResponse(response)
	return &response, err
}

// Run a GPT completion request and return the response
func (this *GPT) LegacyCompletion(request *util.CompletionRequest) (string, error) {
	req := openai.CompletionRequest{
		Model:       request.Model,
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		Prompt:      request.Prompt,
	}

	this.printPrompt(request.Prompt)
	resp, err := this.client.CreateCompletion(request.Ctx, req)
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
	messages := append(gptHistory, openai.ChatCompletionMessage{
		Role:    "user",
		Content: request.Prompt,
	})

	req := openai.ChatCompletionRequest{
		Model:       request.Model,
		Messages:    messages,
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		N:           1,
	}

	return this.doChatCompletion(request.Ctx, req)
}

func (this *GPT) SimpleChatCompletion(request *util.CompletionRequest) (string, error) {
	req := openai.ChatCompletionRequest{
		Model: request.Model,
		Messages: []openai.ChatCompletionMessage{
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
		Temperature: request.Temperature,
		N:           1,
	}

	return this.doChatCompletion(request.Ctx, req)
}

func (this *GPT) doChatCompletion(ctx context.Context, request openai.ChatCompletionRequest) (string, error) {
	this.printPrompt(ChatCompletionRequestMessagesString(request.Messages))
	resp, err := this.client.CreateChatCompletion(ctx, request)
	if err != nil {
		return "", err
	}

	responseText := resp.Choices[0].Message.Content

	this.printResponse(responseText)
	return responseText, nil
}

const GPTEmbeddingsMaxTokens = 8192
const GPTEmbeddingsModel = openai.AdaEmbeddingV2

func withExponentialBackoff(f func() error) error {
	for i := 0; ; i++ {
		err := f()

		if err != nil && strings.Contains(err.Error(), "429:requests") {
			// TODO should probably have a better error detection
			sleepTime := time.Duration(math.Pow(1.6, float64(i+1))) * time.Second
			log.Printf("Rate limited, sleeping for %s\n", sleepTime)
			time.Sleep(sleepTime)

			if i > 6 {
				return fmt.Errorf("Getting 429s from GPT-3, giving up after %d retries", i)
			}
			continue
		}
		return err
	}
}

func (this *GPT) Embeddings(ctx context.Context, input []string) ([][]float32, error) {
	req := openai.EmbeddingRequest{
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

	result := [][]float32{}

	err := withExponentialBackoff(func() error {
		resp, err := this.client.CreateEmbeddings(ctx, req)
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
