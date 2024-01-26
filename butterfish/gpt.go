package butterfish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
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
	client *openai.Client
}

const gptClientTimeout = 300 * time.Second

func NewGPT(token, baseUrl string) *GPT {
	config := openai.DefaultConfig(token)

	config.HTTPClient.Transport = &http.Transport{Proxy: http.ProxyFromEnvironment}

	if baseUrl != "" {
		config.BaseURL = baseUrl
	}

	client := openai.NewClientWithConfig(config)

	return &GPT{
		client: client,
	}
}

// If input can be parsed to JSON, return a nicely formatted and indented
// version of it, otherwise return the original string
func PrettyJSON(input string) string {
	prettyJSON := new(bytes.Buffer)
	err := json.Indent(prettyJSON, []byte(input), "", "  ")
	if err != nil {
		return input
	}
	return prettyJSON.String()
}

func JSONString(input any) string {
	prettyJSON, err := json.Marshal(input)
	if err != nil {
		panic(err)
	}
	return string(prettyJSON)
}

func LogCompletionResponse(resp util.CompletionResponse, id string) {
	box := LoggingBox{
		Title:    "Completion Response " + id,
		Content:  resp.Completion,
		Color:    0,
		Children: []LoggingBox{},
	}

	if resp.FunctionName != "" {
		params := PrettyJSON(resp.FunctionParameters)
		box.Children = []LoggingBox{
			{
				Title:   "Function Call",
				Content: fmt.Sprintf("%s\n%s", resp.FunctionName, params),
				Color:   2,
			},
		}
	}

	if resp.ToolCalls != nil {
		for _, toolCall := range resp.ToolCalls {
			params := PrettyJSON(toolCall.Function.Parameters)
			box.Children = append(box.Children, LoggingBox{
				Title:   "Tool Call",
				Content: fmt.Sprintf("%s  %s\n%s", toolCall.Function.Name, toolCall.Id, params),
				Color:   2,
			})
		}
	}

	PrintLoggingBox(box)
}

func LogCompletionRequest(req openai.CompletionRequest) {
	meta := fmt.Sprintf("model:       %s\ntemperature: %f\nmax_tokens:  %d",
		req.Model, req.Temperature, req.MaxTokens)

	box := LoggingBox{
		Title:   " Completion Request /v1/completions ",
		Content: meta,
		Color:   0,
		Children: []LoggingBox{
			{
				Title:   "Prompt",
				Content: req.Prompt.(string),
				Color:   1,
			},
		},
	}

	PrintLoggingBox(box)
}

// function to accept a string and replace non basic printable ascii characters with
// their hex values
func replaceNonAscii(s string) string {
	out := []rune{}
	for _, r := range s {
		if !(r >= 33 && r < 127) {
			out = append(out, []rune(fmt.Sprintf("\\x%02x", r))...)
		} else {
			out = append(out, r)
		}
	}
	return string(out)
}

func LogChatCompletionRequest(req openai.ChatCompletionRequest) {
	meta := fmt.Sprintf("model:       %s\ntemperature: %f\nmax_tokens:  %d",
		req.Model, req.Temperature, req.MaxTokens)

	historyBoxes := []LoggingBox{}
	for _, message := range req.Messages {
		color := 0
		title := message.Role

		switch message.Role {
		case "user":
			color = 4
		case "assistant":
			color = 5
		case "system":
			color = 6
		case "function":
			color = 3
			title = fmt.Sprintf("%s: %s", message.Role, message.Name)
		case "tool":
			color = 3
			title = fmt.Sprintf("%s: %s %s", message.Role, message.Name, message.ToolCallID)
		}

		historyBox := LoggingBox{
			Title:   title,
			Content: message.Content,
			Color:   color,
		}

		if message.FunctionCall != nil {
			historyBox.Children = []LoggingBox{
				{
					Title:   "Function Call",
					Content: fmt.Sprintf("%s\n%s", message.FunctionCall.Name, message.FunctionCall.Arguments),
					Color:   3,
				},
			}
		}

		if message.ToolCalls != nil {
			for _, tool := range message.ToolCalls {
				historyBox.Children = append(historyBox.Children, LoggingBox{
					Title:   "Tool Call",
					Content: fmt.Sprintf("%s\n%s", tool.Function.Name, tool.Function.Arguments),
					Color:   3,
				})
			}
		}

		historyBoxes = append(historyBoxes, historyBox)
	}

	box := LoggingBox{
		Title:   "Completion Request /v1/chat/completions",
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

	for _, tool := range req.Tools {
		params := PrettyJSON(JSONString(tool.Function.Parameters))
		// list function parameters in a string
		functionBoxes = append(functionBoxes, LoggingBox{
			Title:   tool.Function.Name,
			Content: fmt.Sprintf("%s\n%s", tool.Function.Description, params),
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

func ShellHistoryTypeToRole(t int) string {
	switch t {
	case historyTypeLLMOutput:
		return "assistant"
	case historyTypeFunctionOutput:
		return "function"
	case historyTypeToolOutput:
		return "tool"
	default:
		return "user"
	}
}

func ShellHistoryBlockToGPTChat(block *util.HistoryBlock) *openai.ChatCompletionMessage {
	role := ShellHistoryTypeToRole(block.Type)
	name := ""
	toolCallId := ""
	var function *openai.FunctionCall
	var toolCalls []openai.ToolCall

	if role == "function" {
		// this case means this is a function call response and thus name should
		// be the function name
		name = block.FunctionName
	} else if role == "tool" { // this case means this is a tool call response
		name = block.FunctionName
		toolCallId = block.ToolCallId

	} else if role == "assistant" {
		if block.FunctionName != "" { // this is the model returning a function call
			function = &openai.FunctionCall{
				Name:      block.FunctionName,
				Arguments: block.FunctionParams,
			}
		}
		if block.ToolCalls != nil { // this is the model returning tool calls
			toolCalls = []openai.ToolCall{}
			for _, toolCall := range block.ToolCalls {
				toolCalls = append(toolCalls, openai.ToolCall{
					Type: openai.ToolTypeFunction,
					ID:   toolCall.Id,
					Function: openai.FunctionCall{
						Name:      toolCall.Function.Name,
						Arguments: toolCall.Function.Parameters,
					},
				})
			}
		}

	}

	return &openai.ChatCompletionMessage{
		Role:         role,
		Content:      block.Content,
		Name:         name,
		FunctionCall: function,
		ToolCallID:   toolCallId,
		ToolCalls:    toolCalls,
	}
}

func ShellHistoryBlocksToGPTChat(systemMsg string, blocks []util.HistoryBlock) []openai.ChatCompletionMessage {
	out := []openai.ChatCompletionMessage{
		{
			Role:    "system",
			Content: systemMsg,
		},
	}

	for _, block := range blocks {
		if block.Content == "" && block.FunctionName == "" && block.ToolCalls == nil {
			// skip empty blocks
			continue
		}
		nextBlock := ShellHistoryBlockToGPTChat(&block)
		out = append(out, *nextBlock)
	}

	return out
}

// We're doing completions through the chat API by default, this routes
// to the legacy completion API if the model is the legacy model.
func (this *GPT) Completion(request *util.CompletionRequest) (*util.CompletionResponse, error) {
	var result *util.CompletionResponse
	var err error

	if IsCompletionModel(request.Model) {
		result, err = this.InstructCompletion(request)
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

// If the model is legacy or ends with -instruct then it should use completion
// api, otherwise it should use the chat api.
func IsCompletionModel(modelName string) bool {
	return IsLegacyModel(modelName) || strings.HasSuffix(modelName, "-instruct")
}

// We're doing completions through the chat API by default, this routes
// to the legacy completion API if the model is the legacy model.
func (this *GPT) CompletionStream(request *util.CompletionRequest, writer io.Writer) (*util.CompletionResponse, error) {
	var result *util.CompletionResponse
	var err error

	if IsCompletionModel(request.Model) {
		result, err = this.InstructCompletionStream(request, writer)
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

func (this *GPT) InstructCompletionStream(request *util.CompletionRequest, writer io.Writer) (*util.CompletionResponse, error) {
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

	if request.Verbose {
		LogCompletionRequest(req)
	}
	stream, err := this.client.CreateCompletionStream(request.Ctx, req)
	var id string

	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, err
		}

		callback(response)
		id = response.ID
	}
	fmt.Fprintf(writer, "\n") // GPT doesn't finish with a newline

	response := util.CompletionResponse{
		Completion: strBuilder.String(),
	}

	if request.Verbose {
		LogCompletionResponse(response, id)
	}

	return &response, err
}

func (this *GPT) SimpleChatCompletionStream(request *util.CompletionRequest, writer io.Writer) (*util.CompletionResponse, error) {
	if request.SystemMessage == "" {
		return nil, errors.New("system message required for full chat completion")
	}

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
		Functions:   convertToOpenaiFunctions(request.Functions),
		Tools:       convertToOpenaiTools(request.Tools),
	}

	return this.doChatStreamCompletion(request.Ctx, req, writer, request.Verbose)
}

func convertToOpenaiFunctions(funcs []util.FunctionDefinition) []openai.FunctionDefinition {
	if funcs == nil {
		return nil
	}

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

func convertToOpenaiTools(tools []util.ToolDefinition) []openai.Tool {
	if tools == nil {
		return nil
	}

	out := []openai.Tool{}
	for _, t := range tools {
		tool := openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: openai.FunctionDefinition{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			},
		}
		out = append(out, tool)
	}

	return out
}

func (this *GPT) FullChatCompletionStream(request *util.CompletionRequest, writer io.Writer) (*util.CompletionResponse, error) {
	gptHistory := ShellHistoryBlocksToGPTChat(request.SystemMessage, request.HistoryBlocks)

	if len(gptHistory) == 0 || gptHistory[0].Role != "system" {
		return nil, errors.New("System message required for full chat completion")
	}

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
		Tools:       convertToOpenaiTools(request.Tools),
	}

	return this.doChatStreamCompletion(request.Ctx, req, writer, request.Verbose)
}

const chunkWaitTimeout = 10 * time.Second

func (this *GPT) doChatStreamCompletion(
	ctx context.Context,
	req openai.ChatCompletionRequest,
	printWriter io.Writer,
	verbose bool) (*util.CompletionResponse, error) {

	var responseContent strings.Builder
	var functionName string
	var functionArgs strings.Builder
	var toolCalls []*util.ToolCall

	// We already have a context that sets an overall timeout, but we also
	// want to timeout if we don't get a chunk back for a while.
	// i.e. the overall timeout for the whole request is 60s, the timeout
	// for the first chunk is 5s
	innerCtx, cancel := context.WithCancel(ctx)
	gotChunk := make(chan bool)
	var chunkTimeoutErr error

	// set a goroutine to wait on a timeout or having received a chunk
	go func() {
		select {
		case <-time.After(chunkWaitTimeout):
			chunkTimeoutErr = fmt.Errorf("Timed out waiting for streaming response")
			cancel()

			// if we get a chunk or the context fininshes we don't do anything
		case <-innerCtx.Done():
		case <-gotChunk:
		}
	}()

	firstChunk := true

	callback := func(resp openai.ChatCompletionStreamResponse) {
		if firstChunk {
			gotChunk <- true
			firstChunk = false
			close(gotChunk)
		}

		if resp.Choices == nil || len(resp.Choices) == 0 {
			return
		}

		text := resp.Choices[0].Delta.Content
		functionCall := resp.Choices[0].Delta.FunctionCall
		chunkToolCalls := resp.Choices[0].Delta.ToolCalls

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

		// Handle incremental tool call chunks
		if chunkToolCalls != nil {
			for _, chunkToolCall := range chunkToolCalls {
				if chunkToolCall.Index == nil {
					continue
				}

				// if we haven't seen this tool call before, add empty tool calls
				for len(toolCalls) <= *chunkToolCall.Index {
					toolCalls = append(toolCalls, &util.ToolCall{})
				}

				toolCall := toolCalls[*chunkToolCall.Index]
				id := chunkToolCall.ID
				name := chunkToolCall.Function.Name
				args := chunkToolCall.Function.Arguments

				if id != "" {
					toolCall.Id = id
				}
				if name != "" {
					toolCall.Function.Name += name
					printWriter.Write([]byte(name))
					printWriter.Write([]byte("("))
				}
				if args != "" {
					toolCall.Function.Parameters += args
					printWriter.Write([]byte(args))
				}
			}
		}

		if text == "" {
			return
		}

		printWriter.Write([]byte(text))
		responseContent.WriteString(text)
	}

	if verbose {
		LogChatCompletionRequest(req)
	}
	var stream *openai.ChatCompletionStream

	err := withExponentialBackoff(func() error {
		var innerErr error
		stream, innerErr = this.client.CreateChatCompletionStream(innerCtx, req)
		return innerErr
	})

	// if chunkTimeoutErr is set then err is "context cancelled", which isn't
	// helpful, so we return a more specific error instead
	if chunkTimeoutErr != nil {
		return nil, chunkTimeoutErr
	}

	if err != nil {
		return nil, err
	}

	var id string
	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			if chunkTimeoutErr != nil {
				return nil, chunkTimeoutErr
			}
			return nil, err
		}

		callback(response)
		id = response.ID
	}

	// this doesn't yet handle multiple tool calls
	if functionName != "" || len(toolCalls) > 0 {
		printWriter.Write([]byte(")"))
	}

	fmt.Fprintf(printWriter, "\n") // GPT doesn't finish with a newline

	response := util.CompletionResponse{
		Completion:         responseContent.String(),
		FunctionName:       functionName,
		ToolCalls:          toolCalls,
		FunctionParameters: functionArgs.String(),
	}

	if verbose {
		LogCompletionResponse(response, id)
	}
	return &response, err
}

// Run a GPT completion request and return the response
func (this *GPT) InstructCompletion(request *util.CompletionRequest) (*util.CompletionResponse, error) {
	req := openai.CompletionRequest{
		Model:       request.Model,
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		Prompt:      request.Prompt,
	}

	if request.Verbose {
		LogCompletionRequest(req)
	}

	resp, err := this.client.CreateCompletion(request.Ctx, req)
	if err != nil {
		return nil, err
	}

	if len(resp.Choices) == 0 {
		return nil, errors.New("No completions returned from a completion request with 200 response.")
	}

	text := resp.Choices[0].Text
	// clean whitespace prefix and suffix from text
	text = strings.TrimSpace(text)

	response := util.CompletionResponse{
		Completion: text,
	}

	if request.Verbose {
		LogCompletionResponse(response, resp.ID)
	}
	return &response, nil
}

func (this *GPT) FullChatCompletion(request *util.CompletionRequest) (*util.CompletionResponse, error) {
	gptHistory := ShellHistoryBlocksToGPTChat(request.SystemMessage, request.HistoryBlocks)

	if request.Prompt != "" {
		gptHistory = append(gptHistory, openai.ChatCompletionMessage{
			Role:    "user",
			Content: request.Prompt,
		})
	}

	if len(gptHistory) == 0 || gptHistory[0].Role != "system" {
		return nil, errors.New("System message required for full chat completion")
	}

	req := openai.ChatCompletionRequest{
		Model:       request.Model,
		Messages:    gptHistory,
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		N:           1,
		Functions:   convertToOpenaiFunctions(request.Functions),
	}

	return this.doChatCompletion(request.Ctx, req, request.Verbose)
}

func (this *GPT) SimpleChatCompletion(request *util.CompletionRequest) (*util.CompletionResponse, error) {
	if request.SystemMessage == "" {
		return nil, errors.New("system message is required for full chat completion")
	}

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
		Functions:   convertToOpenaiFunctions(request.Functions),
	}

	return this.doChatCompletion(request.Ctx, req, request.Verbose)
}

func (this *GPT) doChatCompletion(ctx context.Context, request openai.ChatCompletionRequest, verbose bool) (*util.CompletionResponse, error) {
	if verbose {
		LogChatCompletionRequest(request)
	}
	var resp openai.ChatCompletionResponse

	err := withExponentialBackoff(func() error {
		var innerErr error
		resp, innerErr = this.client.CreateChatCompletion(ctx, request)
		return innerErr
	})
	if err != nil {
		return nil, err
	}

	responseText := resp.Choices[0].Message.Content

	response := util.CompletionResponse{
		Completion: responseText,
	}

	funcCall := resp.Choices[0].Message.FunctionCall
	if funcCall != nil {
		response.FunctionName = funcCall.Name
		response.FunctionParameters = funcCall.Arguments
	}

	if verbose {
		LogCompletionResponse(response, resp.ID)
	}
	return &response, nil
}

const GPTEmbeddingsMaxTokens = 8192
const GPTEmbeddingsModel = openai.AdaEmbeddingV2

func withExponentialBackoff(f func() error) error {
	for i := 0; ; i++ {
		err := f()

		if err != nil && strings.Contains(err.Error(), "429") {
			// TODO should probably have a better error detection
			sleepTime := time.Duration(math.Pow(1.6, float64(i+1))) * time.Second
			log.Printf("Rate limited, sleeping for %s\n", sleepTime)
			time.Sleep(sleepTime)

			if i > 3 {
				return fmt.Errorf("Getting 429s from OpenAI API, this means you're hitting the rate limit, giving up after %d retries", i)
			}
			continue
		}
		return err
	}
}

func (this *GPT) Embeddings(ctx context.Context, input []string, verbose bool) ([][]float32, error) {
	req := openai.EmbeddingRequest{
		Input: input,
		Model: GPTEmbeddingsModel,
	}

	if verbose {
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
		fmt.Printf("%s\n", summary)
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
