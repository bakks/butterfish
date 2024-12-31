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
	"strings"
	"time"

	"github.com/bakks/butterfish/util"
	openai "github.com/openai/openai-go"
	option "github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/ssestream"
	"github.com/openai/openai-go/shared"
)

const ERR_429 = "429:insufficient_quota"
const ERR_429_HELP = "You are likely using a free OpenAI account without a subscription activated, this error means you are out of credits. To resolve it, set up a subscription at https://platform.openai.com/account/billing/overview. This requires a credit card and payment, run `butterfish help` for guidance on managing cost. Once you have a subscription set up you must issue a NEW OpenAI token, your previous token will not reflect the subscription."

type GPT struct {
	client *openai.Client
}

func NewGPT(token, baseUrl string) *GPT {
	if baseUrl == "" {
		baseUrl = "https://api.openai.com/v1/"
	}

	client := openai.NewClient(
		option.WithAPIKey(token),
		option.WithBaseURL(baseUrl),
	)

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

func LogCompletionResponse(resp *util.CompletionResponse, id string) {
	box := LoggingBox{
		Title:    "Completion Response " + id,
		Content:  resp.Completion,
		Color:    0,
		Children: []LoggingBox{},
	}

	if resp.ToolCalls != nil {
		for _, toolCall := range resp.ToolCalls {
			params := PrettyJSON(toolCall.Function.Arguments)
			box.Children = append(box.Children, LoggingBox{
				Title:   "Tool Call",
				Content: fmt.Sprintf("%s  %s\n%s", toolCall.Function.Name, toolCall.Id, params),
				Color:   2,
			})
		}
	}

	PrintLoggingBox(box)
}

func LogCompletionRequest(req *openai.CompletionNewParams) {
	meta := fmt.Sprintf("model:       %s\ntemperature: %f\nmax_tokens:  %d",
		req.Model.Value, req.Temperature.Value, req.MaxTokens.Value)

	box := LoggingBox{
		Title:   " Completion Request /v1/completions ",
		Content: meta,
		Color:   0,
		Children: []LoggingBox{
			{
				Title:   "Prompt",
				Content: string(req.Prompt.Value.(shared.UnionString)),
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

func LogChatCompletionRequest(req *openai.ChatCompletionNewParams) {
	meta := fmt.Sprintf("model:       %s\ntemperature: %f\nmax_tokens:  %d",
		req.Model.Value, req.Temperature.Value, req.MaxTokens.Value)

	historyBoxes := []LoggingBox{}
	for _, messageUnion := range req.Messages.Value {
		message := messageUnion.(openai.ChatCompletionMessageParam)
		color := 0
		title := string(message.Role.Value)

		switch message.Role.Value {
		case "user":
			color = 4
		case "assistant":
			color = 5
		case "system":
			color = 6
		case "function":
			panic("function messages no longer supported")
		case "tool":
			color = 3
			title = fmt.Sprintf("%s: %s %s", message.Role, message.Name, message.ToolCallID)
		}

		var content string

		switch message.Content.Value.(type) {
		case string:
			content = message.Content.Value.(string)
		default:
			content = JSONString(message.Content.Value)
		}

		historyBox := LoggingBox{
			Title:   title,
			Content: content,
			Color:   color,
		}

		if message.ToolCalls.Value != nil {
			for _, tool := range message.ToolCalls.Value.([]openai.ToolCall) {
				function := tool.Function.(openai.ChatCompletionMessageToolCallFunctionParam)
				historyBox.Children = append(historyBox.Children, LoggingBox{
					Title:   "Tool Call",
					Content: fmt.Sprintf("%s\n%s", function.Name, function.Arguments),
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

	for _, tool := range req.Tools.Value {
		params := PrettyJSON(JSONString(tool.Function.Value.Parameters))
		// list function parameters in a string
		functionBoxes = append(functionBoxes, LoggingBox{
			Title:   tool.Function.Value.Name.Value,
			Content: fmt.Sprintf("%s\n%s", tool.Function.Value.Description.Value, params),
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

func ShellHistoryBlockToOpenaiMessage(block *util.HistoryBlock) openai.ChatCompletionMessageParam {
	role := ShellHistoryTypeToRole(block.Type)
	var toolCalls []openai.ChatCompletionMessageToolCallParam

	if role == "assistant" {
		if block.ToolCalls != nil { // this is the model returning tool calls
			toolCalls = []openai.ChatCompletionMessageToolCallParam{}
			for _, toolCall := range block.ToolCalls {
				toolCallParam := openai.ChatCompletionMessageToolCallParam{
					Type: openai.F(openai.ChatCompletionMessageToolCallTypeFunction),
					ID:   openai.F(toolCall.Id),
					Function: openai.F(openai.ChatCompletionMessageToolCallFunctionParam{
						Name:      openai.F(toolCall.Function.Name),
						Arguments: openai.F(toolCall.Function.Arguments),
					}),
				}
				toolCalls = append(toolCalls, toolCallParam)
			}
		}

	}

	return openai.ChatCompletionMessageParam{
		Role:       openai.F(openai.ChatCompletionMessageParamRole(role)),
		Content:    openai.F(any(block.Content)),
		ToolCalls:  openai.F(any(toolCalls)),
		ToolCallID: openai.F(block.ToolCallId),
	}
}

func ShellHistoryBlocksToOpenaiMessages(systemMsg string, blocks []util.HistoryBlock) []openai.ChatCompletionMessageParam {
	out := []openai.ChatCompletionMessageParam{
		{
			Role:    openai.F(openai.ChatCompletionMessageParamRole("system")),
			Content: openai.F(any(systemMsg)),
		},
	}

	for _, block := range blocks {
		if block.Content == "" && (block.ToolCalls == nil || len(block.ToolCalls) == 0) {
			// skip empty blocks
			continue
		}
		nextBlock := ShellHistoryBlockToOpenaiMessage(&block)
		out = append(out, nextBlock)
	}

	return out
}

// Do a batch (non-streaming) completion. This will call InstructCompletion if the model is a
// completion model, it will call SimpleChatCompletion if there is only a prompt rather than a
// history, and it will call FullChatCompletion if there is a history.
func (this *GPT) Completion(request *util.CompletionRequest) (*util.CompletionResponse, error) {
	var result *util.CompletionResponse
	var err error

	if IsCompletionModel(request.Model) {
		result, err = this.InstructCompletion(request)
	} else if request.Messages == nil {
		result, err = this.SimpleChatCompletion(request, nil)
	} else {
		result, err = this.FullChatCompletion(request, nil)
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
	return openai.CompletionNewParamsModel(modelName).IsKnown()
}

// Do a streaming completion. This will call InstructCompletion if the model is a
// completion model, it will call SimpleChatCompletion if there is only a prompt rather than a
// history, and it will call FullChatCompletion if there is a history.
func (this *GPT) CompletionStream(request *util.CompletionRequest, writer io.Writer) (*util.CompletionResponse, error) {
	var result *util.CompletionResponse
	var err error

	if IsCompletionModel(request.Model) {
		result, err = this.InstructCompletionStream(request, writer)
	} else if request.Messages == nil {
		result, err = this.SimpleChatCompletion(request, writer)
	} else {
		result, err = this.FullChatCompletion(request, writer)
	}

	// This error means the user needs to set up a subscription, give advice
	if err != nil && strings.Contains(err.Error(), ERR_429) {
		err = fmt.Errorf("%s\n\n%s", err.Error(), ERR_429_HELP)
	}

	return result, err
}

// Run a GPT completion request and return the response
func (this *GPT) InstructCompletion(request *util.CompletionRequest) (*util.CompletionResponse, error) {
	params := openai.CompletionNewParams{
		Prompt: openai.F(openai.CompletionNewParamsPromptUnion(
			shared.UnionString(request.Prompt),
		)),
		Model:       openai.F(openai.CompletionNewParamsModel(request.Model)),
		MaxTokens:   openai.F(int64(request.MaxTokens)),
		Temperature: openai.F(float64(request.Temperature)),
	}

	if request.Verbose {
		LogCompletionRequest(&params)
	}

	resp, err := this.client.Completions.New(request.Ctx, params)
	if err != nil {
		return nil, err
	}

	if len(resp.Choices) == 0 {
		return nil, errors.New("No completions returned from a completion request with 200 response.")
	}

	text := resp.Choices[0].Text
	// clean whitespace prefix and suffix from text
	text = strings.TrimSpace(text)

	response := &util.CompletionResponse{
		Completion: text,
	}

	if request.Verbose {
		LogCompletionResponse(response, resp.ID)
	}
	return response, nil
}

func (this *GPT) InstructCompletionStream(
	request *util.CompletionRequest,
	writer io.Writer,
) (*util.CompletionResponse, error) {
	params := openai.CompletionNewParams{
		Prompt: openai.F(openai.CompletionNewParamsPromptUnion(
			shared.UnionString(request.Prompt),
		)),
		Model:       openai.F(openai.CompletionNewParamsModel(request.Model)),
		MaxTokens:   openai.F(int64(request.MaxTokens)),
		Temperature: openai.F(float64(request.Temperature)),
	}

	if request.Verbose {
		LogCompletionRequest(&params)
	}

	stream := this.client.Completions.NewStreaming(request.Ctx, params)
	strBuilder := strings.Builder{}
	var id string

	for stream.Next() {
		chunk := stream.Current()
		id = chunk.ID
		if chunk.Choices == nil || len(chunk.Choices) == 0 {
			continue
		}

		err := stream.Err()
		if err != nil {
			return nil, err
		}

		text := chunk.Choices[0].Text
		writer.Write([]byte(text))
		strBuilder.WriteString(text)
	}

	fmt.Fprintf(writer, "\n") // GPT doesn't finish with a newline

	response := util.CompletionResponse{
		Completion: strBuilder.String(),
	}

	if request.Verbose {
		LogCompletionResponse(&response, id)
	}

	return &response, nil
}

func convertToOpenaiFunctions(funcs []util.FunctionDefinition) []openai.ChatCompletionToolParam {
	if funcs == nil {
		return nil
	}

	out := []openai.ChatCompletionToolParam{}
	for _, f := range funcs {
		toolParam := openai.ChatCompletionToolParam{
			Type: openai.F(openai.ChatCompletionToolTypeFunction),
			Function: openai.F(openai.FunctionDefinitionParam{
				Name:        openai.String(f.Name),
				Description: openai.String(f.Description),
				Parameters:  openai.F(openai.FunctionParameters(f.Parameters)),
			}),
		}
		out = append(out, toolParam)
	}

	return out
}

func (this *GPT) FullChatCompletion(request *util.CompletionRequest, writer io.Writer) (*util.CompletionResponse, error) {
	messages := ShellHistoryBlocksToOpenaiMessages(request.SystemMessage, request.Messages)

	if len(messages) == 0 || messages[0].Role.Value != "system" {
		return nil, errors.New("System message required for full chat completion")
	}

	if request.Prompt != "" {
		messages = append(messages, openai.ChatCompletionMessageParam{
			Role:    openai.F(openai.ChatCompletionMessageParamRole("user")),
			Content: openai.F(any(request.Prompt)),
		})
	}

	tools := convertToOpenaiFunctions(request.Functions)

	params := openai.ChatCompletionNewParams{
		Messages:    openai.F(convertMessagesToUnion(messages)),
		Model:       openai.F(openai.ChatModel(request.Model)),
		MaxTokens:   openai.F(request.MaxTokens),
		Temperature: openai.F(request.Temperature),
		Tools:       openai.F(tools),
	}

	if writer == nil {
		return this.doChatCompletion(request.Ctx, params, request.Verbose)
	}
	return this.doChatStreamCompletion(request.Ctx, params, writer, request.TokenTimeout, request.Verbose)
}

func toCompletionResponse(chatCompletion *openai.ChatCompletion) *util.CompletionResponse {
	if len(chatCompletion.Choices) == 0 {
		return nil
	}

	responseText := chatCompletion.Choices[0].Message.Content

	response := util.CompletionResponse{
		Completion: responseText,
	}

	response.ToolCalls = convertFromOpenaiToolCalls(chatCompletion.Choices[0].Message.ToolCalls)

	return &response
}

func (this *GPT) doChatCompletion(
	ctx context.Context,
	params openai.ChatCompletionNewParams,
	verbose bool,
) (*util.CompletionResponse, error) {
	if verbose {
		LogChatCompletionRequest(&params)
	}
	var resp *openai.ChatCompletion

	err := withExponentialBackoff(func() error {
		var innerErr error
		resp, innerErr = this.client.Chat.Completions.New(ctx, params)
		return innerErr
	})
	if err != nil {
		return nil, err
	}

	response := toCompletionResponse(resp)
	if verbose {
		LogCompletionResponse(response, resp.ID)
	}
	return response, nil
}

func (this *GPT) doChatStreamCompletion(
	ctx context.Context,
	params openai.ChatCompletionNewParams,
	printWriter io.Writer,
	tokenTimeout time.Duration, // max time before first chunk and between chunks
	verbose bool,
) (*util.CompletionResponse, error) {
	// We already have a context that sets an overall timeout, but we also
	// want to timeout if we don't get a chunk back for a while.
	// i.e. the overall timeout for the whole request is 60s, the timeout
	// for the first chunk is 5s
	innerCtx, cancel := context.WithCancel(ctx)
	gotChunk := make(chan bool)
	defer close(gotChunk)
	var chunkTimeoutErr error
	var toolCalls []*util.ToolCall

	// set a goroutine to wait on a timeout or having received a chunk
	timeoutRoutine := func() {
		if tokenTimeout == 0 {
			panic("should not be called")
		}

		select {
		case <-time.After(tokenTimeout):
			chunkTimeoutErr = fmt.Errorf("Timed out waiting for streaming response, this call set a timeout of %v between streaming tokens, set by the --token-timeout (-z) parameter.", tokenTimeout)
			cancel()

			// if we get a chunk or the context fininshes we don't do anything
		case <-innerCtx.Done():
		case <-gotChunk:
		}
	}

	if tokenTimeout > 0 {
		go timeoutRoutine()
	}

	callback := func(chunk openai.ChatCompletionChunk) {
		if tokenTimeout > 0 {
			gotChunk <- true
			go timeoutRoutine()
		}

		if chunk.Choices == nil || len(chunk.Choices) == 0 {
			return
		}

		text := chunk.Choices[0].Delta.Content
		chunkToolCalls := chunk.Choices[0].Delta.ToolCalls

		// Handle incremental tool call chunks
		if chunkToolCalls != nil {
			for _, chunkToolCall := range chunkToolCalls {
				// if we haven't seen this tool call before, add empty tool calls
				index := int(chunkToolCall.Index)
				for len(toolCalls) <= index {
					toolCalls = append(toolCalls, &util.ToolCall{})
				}

				toolCall := toolCalls[index]
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
					toolCall.Function.Arguments += args
					printWriter.Write([]byte(args))
				}
			}
		}

		if text == "" {
			return
		}

		printWriter.Write([]byte(text))
	}

	if verbose {
		LogChatCompletionRequest(&params)
	}
	var stream *ssestream.Stream[openai.ChatCompletionChunk]

	err := withExponentialBackoff(func() error {
		stream = this.client.Chat.Completions.NewStreaming(innerCtx, params)
		return nil
	})

	// if chunkTimeoutErr is set then err is "context cancelled", which isn't
	// helpful, so we return a more specific error instead
	if chunkTimeoutErr != nil {
		return nil, chunkTimeoutErr
	}

	if err != nil {
		return nil, err
	}

	acc := openai.ChatCompletionAccumulator{}

	for stream.Next() {
		chunk := stream.Current()
		err = stream.Err()

		if err != nil {
			if chunkTimeoutErr != nil {
				return nil, chunkTimeoutErr
			}
			return nil, err
		}

		if !acc.AddChunk(chunk) {
			return nil, errors.New("Failed to accumulate chunk")
		}
		callback(chunk)
	}

	// this doesn't yet handle multiple tool calls
	if len(toolCalls) > 0 {
		printWriter.Write([]byte(")"))
	}

	response := toCompletionResponse(&acc.ChatCompletion)
	if verbose {
		LogCompletionResponse(response, acc.ChatCompletion.ID)
	}
	return response, err
}

func convertMessagesToUnion(x []openai.ChatCompletionMessageParam) []openai.ChatCompletionMessageParamUnion {
	out := []openai.ChatCompletionMessageParamUnion{}
	for _, v := range x {
		out = append(out, openai.ChatCompletionMessageParamUnion(v))
	}
	return out
}

func (this *GPT) SimpleChatCompletion(
	request *util.CompletionRequest,
	writer io.Writer,
) (*util.CompletionResponse, error) {
	newReq := *request
	if request.SystemMessage == "" {
		return nil, errors.New("system message is required for full chat completion")
	}

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(request.SystemMessage),
	}

	if request.Prompt != "" {
		messages = append(messages, openai.UserMessage(request.Prompt))
	}

	newReq.Messages = nil
	newReq.SystemMessage = ""
	newReq.Prompt = ""

	return this.FullChatCompletion(&newReq, writer)
}

func convertFromOpenaiToolCalls(toolCalls []openai.ChatCompletionMessageToolCall) []*util.ToolCall {
	out := []*util.ToolCall{}
	for _, toolCall := range toolCalls {
		out = append(out, &util.ToolCall{
			Id: toolCall.ID,
			Function: util.FunctionCall{
				Name:      toolCall.Function.Name,
				Arguments: toolCall.Function.Arguments,
			},
		})
	}
	return out
}

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
