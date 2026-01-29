package butterfish

import (
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
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

const ERR_429 = "429:insufficient_quota"
const ERR_429_HELP = "You are likely using a free OpenAI account without a subscription activated, this error means you are out of credits. To resolve it, set up a subscription at https://platform.openai.com/account/billing/overview. This requires a credit card and payment, run `butterfish help` for guidance on managing cost. Once you have a subscription set up you must issue a NEW OpenAI token, your previous token will not reflect the subscription."

type OpenAIClient struct {
	client openai.Client
}

func NewOpenAIClient(token, baseURL string) *OpenAIClient {
	opts := []option.RequestOption{
		option.WithAPIKey(token),
	}

	normalizedBaseURL := normalizeBaseURL(baseURL)
	if normalizedBaseURL != "" {
		opts = append(opts, option.WithBaseURL(normalizedBaseURL))
	}

	client := openai.NewClient(opts...)
	return &OpenAIClient{
		client: client,
	}
}

func normalizeBaseURL(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return ""
	}

	baseURL = strings.TrimSuffix(baseURL, "/")
	if strings.HasSuffix(baseURL, "/responses") {
		return strings.TrimSuffix(baseURL, "/responses")
	}
	return baseURL
}

func withExponentialBackoff(f func() error) error {
	for i := 0; ; i++ {
		err := f()

		if err != nil && strings.Contains(err.Error(), "429") {
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

func buildTools(functions []util.FunctionDefinition, tools []util.ToolDefinition) []responses.ToolUnionParam {
	toolParams := make([]responses.ToolUnionParam, 0, len(functions)+len(tools))

	for _, fn := range functions {
		toolParams = append(toolParams, responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:        fn.Name,
				Description: param.NewOpt(fn.Description),
				Parameters:  fn.Parameters,
				Strict:      param.NewOpt(true),
			},
		})
	}

	for _, tool := range tools {
		if tool.Type != "function" {
			continue
		}
		fn := tool.Function
		toolParams = append(toolParams, responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:        fn.Name,
				Description: param.NewOpt(fn.Description),
				Parameters:  fn.Parameters,
				Strict:      param.NewOpt(true),
			},
		})
	}

	return toolParams
}

func roleForHistoryBlock(block *util.HistoryBlock) responses.EasyInputMessageRole {
	switch block.Type {
	case historyTypeLLMOutput:
		return responses.EasyInputMessageRoleAssistant
	default:
		return responses.EasyInputMessageRoleUser
	}
}

func buildInputItems(request *util.CompletionRequest) responses.ResponseInputParam {
	items := responses.ResponseInputParam{}

	for _, block := range request.HistoryBlocks {
		if block.Type == historyTypeFunctionOutput || block.Type == historyTypeToolOutput {
			if block.ToolCallId == "" {
				continue
			}
			items = append(items, responses.ResponseInputItemParamOfFunctionCallOutput(
				block.ToolCallId,
				block.Content,
			))
			continue
		}

		if block.FunctionName != "" && block.FunctionParams != "" {
			callID := block.ToolCallId
			if callID == "" {
				callID = block.FunctionName
			}
			items = append(items, responses.ResponseInputItemParamOfFunctionCall(
				block.FunctionParams,
				callID,
				block.FunctionName,
			))
		}

		if len(block.ToolCalls) > 0 {
			for _, toolCall := range block.ToolCalls {
				callID := toolCall.Id
				if callID == "" {
					callID = toolCall.Function.Name
				}
				items = append(items, responses.ResponseInputItemParamOfFunctionCall(
					toolCall.Function.Parameters,
					callID,
					toolCall.Function.Name,
				))
			}
		}

		if block.Content == "" {
			continue
		}
		items = append(items, responses.ResponseInputItemParamOfMessage(
			block.Content,
			roleForHistoryBlock(&block),
		))
	}

	if request.Prompt != "" {
		items = append(items, responses.ResponseInputItemParamOfMessage(
			request.Prompt,
			responses.EasyInputMessageRoleUser,
		))
	}

	return items
}

func buildResponseParams(request *util.CompletionRequest) responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(request.Model),
	}

	if request.SystemMessage != "" {
		params.Instructions = param.NewOpt(request.SystemMessage)
	}
	if request.MaxTokens > 0 {
		params.MaxOutputTokens = param.NewOpt(int64(request.MaxTokens))
	}
	params.Temperature = param.NewOpt(float64(request.Temperature))

	inputItems := buildInputItems(request)
	if len(inputItems) > 0 {
		params.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: inputItems}
	}

	tools := buildTools(request.Functions, request.Tools)
	if len(tools) > 0 {
		params.Tools = tools
	}

	return params
}

func formatJSONLog(value any) string {
	pretty, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("json marshal error: %v", err)
	}
	return string(pretty)
}

func formatInputItemBox(item responses.ResponseInputItemUnionParam, index int) LoggingBox {
	titlePrefix := fmt.Sprintf("Input %d", index+1)

	if item.OfMessage != nil {
		role := string(item.OfMessage.Role)
		roleColor := 2
		switch role {
		case "assistant":
			roleColor = 3
		case "system":
			roleColor = 1
		}
		content := ""
		if !param.IsOmitted(item.OfMessage.Content.OfString) {
			content = item.OfMessage.Content.OfString.Value
		} else if !param.IsOmitted(item.OfMessage.Content.OfInputItemContentList) {
			content = formatJSONLog(item.OfMessage.Content.OfInputItemContentList)
		} else {
			content = formatJSONLog(item.OfMessage)
		}
		return LoggingBox{
			Title:   fmt.Sprintf("%s: message(%s)", titlePrefix, role),
			Content: content,
			Color:   roleColor,
		}
	}

	if item.OfFunctionCall != nil {
		return LoggingBox{
			Title: fmt.Sprintf("%s: function_call(%s)", titlePrefix, item.OfFunctionCall.Name),
			Content: fmt.Sprintf("call_id: %s\narguments: %s",
				item.OfFunctionCall.CallID,
				item.OfFunctionCall.Arguments,
			),
			Color: 2,
		}
	}

	if item.OfFunctionCallOutput != nil {
		output := ""
		if !param.IsOmitted(item.OfFunctionCallOutput.Output.OfString) {
			output = item.OfFunctionCallOutput.Output.OfString.Value
		} else if !param.IsOmitted(item.OfFunctionCallOutput.Output.OfResponseFunctionCallOutputItemArray) {
			output = formatJSONLog(item.OfFunctionCallOutput.Output.OfResponseFunctionCallOutputItemArray)
		} else {
			output = formatJSONLog(item.OfFunctionCallOutput.Output)
		}
		return LoggingBox{
			Title: fmt.Sprintf("%s: function_call_output", titlePrefix),
			Content: fmt.Sprintf("call_id: %s\noutput: %s",
				item.OfFunctionCallOutput.CallID,
				output,
			),
			Color: 2,
		}
	}

	if item.OfInputMessage != nil {
		return LoggingBox{
			Title:   fmt.Sprintf("%s: input_message", titlePrefix),
			Content: formatJSONLog(item.OfInputMessage),
			Color:   2,
		}
	}

	return LoggingBox{
		Title:   fmt.Sprintf("%s: item", titlePrefix),
		Content: formatJSONLog(item),
		Color:   2,
	}
}

func logCompletionRequest(request *util.CompletionRequest, inputItems responses.ResponseInputParam, tools []responses.ToolUnionParam) {
	box := LoggingBox{
		Title: "LLM Request",
		Content: fmt.Sprintf("model: %s\nmax_tokens: %d\ntemperature: %.2f",
			request.Model,
			request.MaxTokens,
			request.Temperature,
		),
		Color: 0,
	}

	if request.SystemMessage != "" {
		box.Children = append(box.Children, LoggingBox{
			Title:   "Instructions",
			Content: request.SystemMessage,
			Color:   1,
		})
	}

	if len(inputItems) > 0 {
		for i, item := range inputItems {
			box.Children = append(box.Children, formatInputItemBox(item, i))
		}
	}

	if len(tools) > 0 {
		box.Children = append(box.Children, LoggingBox{
			Title:   "Tools",
			Content: formatJSONLog(tools),
			Color:   3,
		})
	}

	PrintLoggingBox(box)
}

func toolCallsFromOutputItems(items []responses.ResponseOutputItemUnion) []*util.ToolCall {
	var toolCalls []*util.ToolCall
	for _, item := range items {
		if item.Type != "function_call" {
			continue
		}
		call := item.AsFunctionCall()
		toolCalls = append(toolCalls, &util.ToolCall{
			Id:   call.CallID,
			Type: "function",
			Function: util.FunctionCall{
				Name:       call.Name,
				Parameters: call.Arguments,
			},
		})
	}
	return toolCalls
}

func finalizeCompletionResponse(completion string, toolCalls []*util.ToolCall) *util.CompletionResponse {
	resp := &util.CompletionResponse{
		Completion: completion,
		ToolCalls:  toolCalls,
	}
	if len(toolCalls) > 0 {
		resp.FunctionName = toolCalls[0].Function.Name
		resp.FunctionParameters = toolCalls[0].Function.Parameters
	}
	return resp
}

func (this *OpenAIClient) Completion(request *util.CompletionRequest) (*util.CompletionResponse, error) {
	params := buildResponseParams(request)
	if request.Verbose {
		inputItems := buildInputItems(request)
		tools := buildTools(request.Functions, request.Tools)
		logCompletionRequest(request, inputItems, tools)
	}
	var response *responses.Response

	err := withExponentialBackoff(func() error {
		var innerErr error
		response, innerErr = this.client.Responses.New(request.Ctx, params)
		return innerErr
	})
	if err != nil {
		if strings.Contains(err.Error(), ERR_429) {
			return nil, errors.New(ERR_429_HELP)
		}
		return nil, err
	}

	toolCalls := toolCallsFromOutputItems(response.Output)
	final := finalizeCompletionResponse(response.OutputText(), toolCalls)
	if request.Verbose {
		responseText := response.OutputText()
		if response.ID != "" {
			responseText = fmt.Sprintf("response_id: %s\n\n%s", response.ID, responseText)
		}
		box := LoggingBox{
			Title:   "LLM Response",
			Content: responseText,
			Color:   0,
		}
		if len(toolCalls) > 0 {
			box.Children = append(box.Children, LoggingBox{
				Title:   "Tool Calls",
				Content: formatJSONLog(toolCalls),
				Color:   1,
			})
		}
		PrintLoggingBox(box)
	} else if response != nil && response.ID != "" {
		log.Printf("LLM response id: %s", response.ID)
	}
	return final, nil
}

type streamToolCallInfo struct {
	CallID    string
	Name      string
	Arguments strings.Builder
}

func (this *OpenAIClient) CompletionStream(request *util.CompletionRequest, writer io.Writer) (*util.CompletionResponse, error) {
	params := buildResponseParams(request)
	if request.Verbose {
		inputItems := buildInputItems(request)
		tools := buildTools(request.Functions, request.Tools)
		logCompletionRequest(request, inputItems, tools)
	}

	var stream *ssestream.Stream[responses.ResponseStreamEventUnion]
	var innerErr error

	// We already have a context that sets an overall timeout, but we also want to
	// timeout if we don't get a chunk back for a while.
	innerCtx, cancel := context.WithCancel(request.Ctx)
	gotChunk := make(chan bool)
	defer close(gotChunk)
	var chunkTimeoutErr error

	timeoutRoutine := func() {
		if request.TokenTimeout == 0 {
			panic("should not be called")
		}

		select {
		case <-time.After(request.TokenTimeout):
			chunkTimeoutErr = fmt.Errorf("Timed out waiting for streaming response, this call set a timeout of %v between streaming token responses, set by the --token-timeout (-z) parameter.", request.TokenTimeout)
			cancel()
		case <-innerCtx.Done():
		case <-gotChunk:
		}
	}

	if request.TokenTimeout > 0 {
		go timeoutRoutine()
	}

	err := withExponentialBackoff(func() error {
		stream = this.client.Responses.NewStreaming(innerCtx, params)
		innerErr = stream.Err()
		return innerErr
	})
	if err != nil {
		if chunkTimeoutErr != nil {
			return nil, chunkTimeoutErr
		}
		if strings.Contains(err.Error(), ERR_429) {
			return nil, errors.New(ERR_429_HELP)
		}
		return nil, err
	}
	defer stream.Close()

	var completion strings.Builder
	toolCalls := map[string]*streamToolCallInfo{}
	toolCallOrder := []string{}
	responseID := ""
	var completedResponse *responses.Response

	for stream.Next() {
		if request.TokenTimeout > 0 {
			gotChunk <- true
			go timeoutRoutine()
		}

		event := stream.Current()
		switch event.Type {
		case "response.created":
			created := event.AsResponseCreated()
			if responseID == "" {
				responseID = created.Response.ID
			}
		case "response.completed":
			completed := event.AsResponseCompleted()
			if responseID == "" {
				responseID = completed.Response.ID
			}
			completedResponse = &completed.Response
		case "response.output_text.delta":
			delta := event.AsResponseOutputTextDelta()
			if delta.Delta != "" {
				writer.Write([]byte(delta.Delta))
				completion.WriteString(delta.Delta)
			}
		case "response.output_item.added":
			added := event.AsResponseOutputItemAdded()
			if added.Item.Type == "function_call" {
				call := added.Item.AsFunctionCall()
				if call.ID == "" {
					continue
				}
				info := &streamToolCallInfo{
					CallID: call.CallID,
					Name:   call.Name,
				}
				if call.Arguments != "" {
					info.Arguments.WriteString(call.Arguments)
				}
				toolCalls[call.ID] = info
				toolCallOrder = append(toolCallOrder, call.ID)
			}
		case "response.output_item.done":
			done := event.AsResponseOutputItemDone()
			if done.Item.Type == "function_call" {
				call := done.Item.AsFunctionCall()
				if call.ID == "" {
					continue
				}
				info, ok := toolCalls[call.ID]
				if !ok {
					info = &streamToolCallInfo{}
					toolCalls[call.ID] = info
					toolCallOrder = append(toolCallOrder, call.ID)
				}
				if info.CallID == "" {
					info.CallID = call.CallID
				}
				if info.Name == "" {
					info.Name = call.Name
				}
				if call.Arguments != "" {
					info.Arguments.Reset()
					info.Arguments.WriteString(call.Arguments)
				}
			}
		case "response.function_call_arguments.delta":
			delta := event.AsResponseFunctionCallArgumentsDelta()
			info, ok := toolCalls[delta.ItemID]
			if ok && delta.Delta != "" {
				if info.CallID == "" {
					info.CallID = delta.ItemID
				}
				info.Arguments.WriteString(delta.Delta)
			}
		case "response.function_call_arguments.done":
			done := event.AsResponseFunctionCallArgumentsDone()
			info, ok := toolCalls[done.ItemID]
			if !ok {
				info = &streamToolCallInfo{}
				toolCalls[done.ItemID] = info
				toolCallOrder = append(toolCallOrder, done.ItemID)
			}
			if info.CallID == "" {
				info.CallID = done.ItemID
			}
			info.Name = done.Name
			if done.Arguments != "" {
				info.Arguments.Reset()
				info.Arguments.WriteString(done.Arguments)
			}
		}
	}

	if stream.Err() != nil {
		if chunkTimeoutErr != nil {
			return nil, chunkTimeoutErr
		}
		if strings.Contains(stream.Err().Error(), ERR_429) {
			return nil, errors.New(ERR_429_HELP)
		}
		return nil, stream.Err()
	}

	if chunkTimeoutErr != nil {
		return nil, chunkTimeoutErr
	}

	toolCallResults := make([]*util.ToolCall, 0, len(toolCallOrder))
	for _, id := range toolCallOrder {
		info, ok := toolCalls[id]
		if !ok {
			continue
		}
		toolCallResults = append(toolCallResults, &util.ToolCall{
			Id:   info.CallID,
			Type: "function",
			Function: util.FunctionCall{
				Name:       info.Name,
				Parameters: info.Arguments.String(),
			},
		})
	}

	if completedResponse != nil && len(toolCallResults) == 0 {
		toolCallResults = toolCallsFromOutputItems(completedResponse.Output)
		if completion.Len() == 0 {
			completion.WriteString(completedResponse.OutputText())
		}
	}

	final := finalizeCompletionResponse(completion.String(), toolCallResults)
	if request.Verbose {
		responseText := completion.String()
		if responseID != "" {
			responseText = fmt.Sprintf("response_id: %s\n\n%s", responseID, responseText)
		}
		box := LoggingBox{
			Title:   "LLM Response",
			Content: responseText,
			Color:   0,
		}
		if len(toolCallResults) > 0 {
			box.Children = append(box.Children, LoggingBox{
				Title:   "Tool Calls",
				Content: formatJSONLog(toolCallResults),
				Color:   1,
			})
		}
		PrintLoggingBox(box)
	} else if responseID != "" {
		log.Printf("LLM response id: %s", responseID)
	}
	return final, nil
}
