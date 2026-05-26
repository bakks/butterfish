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
	"sync"
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

	reasoningMutex       sync.RWMutex
	reasoningUnsupported map[string]bool

	serviceTierMutex       sync.RWMutex
	serviceTierUnsupported map[string]bool
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
		client:                 client,
		reasoningUnsupported:   make(map[string]bool),
		serviceTierUnsupported: make(map[string]bool),
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

func normalizeModelKey(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

func normalizeServiceTier(tier string) string {
	tier = strings.ToLower(strings.TrimSpace(tier))
	if tier == "fast" {
		return "priority"
	}
	return tier
}

func (this *OpenAIClient) isReasoningUnsupportedForModel(model string) bool {
	key := normalizeModelKey(model)
	if key == "" {
		return false
	}

	this.reasoningMutex.RLock()
	unsupported := this.reasoningUnsupported[key]
	this.reasoningMutex.RUnlock()
	return unsupported
}

func (this *OpenAIClient) markReasoningUnsupportedForModel(model string) {
	key := normalizeModelKey(model)
	if key == "" {
		return
	}

	this.reasoningMutex.Lock()
	this.reasoningUnsupported[key] = true
	this.reasoningMutex.Unlock()
}

func isReasoningUnsupportedError(err error) bool {
	if err == nil {
		return false
	}

	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == 400 {
			paramLower := strings.ToLower(apiErr.Param)
			if strings.Contains(paramLower, "reasoning") {
				return true
			}
			msgLower := strings.ToLower(apiErr.Message)
			if strings.Contains(msgLower, "reasoning") &&
				(strings.Contains(msgLower, "not supported") ||
					strings.Contains(msgLower, "unsupported") ||
					strings.Contains(msgLower, "unknown parameter")) {
				return true
			}
		}
		return false
	}

	errLower := strings.ToLower(err.Error())
	return strings.Contains(errLower, "reasoning") &&
		(strings.Contains(errLower, "not supported") ||
			strings.Contains(errLower, "unsupported") ||
			strings.Contains(errLower, "unknown parameter"))
}

func (this *OpenAIClient) isServiceTierUnsupported(tier string) bool {
	tier = normalizeServiceTier(tier)
	if tier == "" {
		return false
	}

	this.serviceTierMutex.RLock()
	unsupported := this.serviceTierUnsupported[tier]
	this.serviceTierMutex.RUnlock()
	return unsupported
}

func (this *OpenAIClient) markServiceTierUnsupported(tier string) {
	tier = normalizeServiceTier(tier)
	if tier == "" {
		return
	}

	this.serviceTierMutex.Lock()
	this.serviceTierUnsupported[tier] = true
	this.serviceTierMutex.Unlock()
}

func isServiceTierUnsupportedError(err error) bool {
	if err == nil {
		return false
	}

	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 {
			paramLower := strings.ToLower(apiErr.Param)
			if strings.Contains(paramLower, "service_tier") {
				return true
			}
			msgLower := strings.ToLower(apiErr.Message)
			if strings.Contains(msgLower, "service_tier") &&
				(strings.Contains(msgLower, "not supported") ||
					strings.Contains(msgLower, "unsupported") ||
					strings.Contains(msgLower, "invalid") ||
					strings.Contains(msgLower, "unknown parameter")) {
				return true
			}
		}
		return false
	}

	errLower := strings.ToLower(err.Error())
	return strings.Contains(errLower, "service_tier") &&
		(strings.Contains(errLower, "not supported") ||
			strings.Contains(errLower, "unsupported") ||
			strings.Contains(errLower, "invalid") ||
			strings.Contains(errLower, "unknown parameter"))
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
		switch tool.Type {
		case "function":
			fn := tool.Function
			toolParams = append(toolParams, responses.ToolUnionParam{
				OfFunction: &responses.FunctionToolParam{
					Name:        fn.Name,
					Description: param.NewOpt(fn.Description),
					Parameters:  fn.Parameters,
					Strict:      param.NewOpt(true),
				},
			})
		case "shell":
			shellTool := responses.NewFunctionShellToolParam()
			toolParams = append(toolParams, responses.ToolUnionParam{
				OfShell: &shellTool,
			})
		}
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
	seenFunctionCalls := map[string]bool{}
	seenShellCalls := map[string]bool{}

	for _, block := range request.HistoryBlocks {
		if block.FunctionName != "" {
			callID := block.ToolCallId
			if callID == "" {
				callID = block.FunctionName
			}
			seenFunctionCalls[callID] = true
		}
		if len(block.ToolCalls) > 0 {
			for _, toolCall := range block.ToolCalls {
				callID := toolCall.Id
				if callID == "" {
					callID = toolCall.Function.Name
				}
				seenFunctionCalls[callID] = true
			}
		}
		if block.ShellCall != nil && block.ShellCall.CallID != "" {
			seenShellCalls[block.ShellCall.CallID] = true
		}
	}

	for _, block := range request.HistoryBlocks {
		if block.Type == historyTypeFunctionOutput {
			callID := block.ToolCallId
			if callID == "" {
				callID = block.FunctionName
			}
			if callID == "" {
				continue
			}
			if !seenFunctionCalls[callID] {
				continue
			}
			items = append(items, responses.ResponseInputItemParamOfFunctionCallOutput(
				callID,
				block.Content,
			))
			continue
		}
		if block.Type == historyTypeToolOutput && block.ToolType == "shell" && block.ShellCallOutput != nil {
			shellOutput := truncateShellCallOutputForResponses(block.ShellCallOutput)
			if shellOutput.CallID == "" || !seenShellCalls[shellOutput.CallID] {
				continue
			}
			var outputs []responses.ResponseFunctionShellCallOutputContentParam
			for _, out := range shellOutput.Output {
				content := responses.ResponseFunctionShellCallOutputContentParam{
					Stdout: out.Stdout,
					Stderr: out.Stderr,
				}
				switch out.Outcome.Type {
				case "timeout":
					timeout := responses.NewResponseFunctionShellCallOutputContentOutcomeTimeoutParam()
					content.Outcome.OfTimeout = &timeout
				default:
					exit := responses.ResponseFunctionShellCallOutputContentOutcomeExitParam{
						ExitCode: int64(out.Outcome.ExitCode),
					}
					content.Outcome.OfExit = &exit
				}
				outputs = append(outputs, content)
			}

			var shellCallOutput responses.ResponseInputItemShellCallOutputParam
			shellCallOutput.CallID = shellOutput.CallID
			shellCallOutput.Output = outputs
			if shellOutput.MaxOutputLength > 0 {
				shellCallOutput.MaxOutputLength = param.NewOpt(shellOutput.MaxOutputLength)
			}
			items = append(items, responses.ResponseInputItemUnionParam{OfShellCallOutput: &shellCallOutput})
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

		if block.ShellCall != nil {
			action := responses.ResponseInputItemShellCallActionParam{
				Commands: block.ShellCall.Commands,
			}
			if block.ShellCall.TimeoutMs > 0 {
				action.TimeoutMs = param.NewOpt(block.ShellCall.TimeoutMs)
			}
			if block.ShellCall.MaxOutputLength > 0 {
				action.MaxOutputLength = param.NewOpt(block.ShellCall.MaxOutputLength)
			}
			items = append(items, responses.ResponseInputItemParamOfShellCall(action, block.ShellCall.CallID))
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

func buildResponseParams(request *util.CompletionRequest, reasoningEffort string) responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(request.Model),
	}
	serviceTier := normalizeServiceTier(request.ServiceTier)

	if request.SystemMessage != "" {
		params.Instructions = param.NewOpt(request.SystemMessage)
	}
	if request.MaxTokens > 0 {
		params.MaxOutputTokens = param.NewOpt(int64(request.MaxTokens))
	}
	if serviceTier != "" {
		params.ServiceTier = responses.ResponseNewParamsServiceTier(serviceTier)
	}
	if reasoningEffort != "" {
		params.Reasoning = shared.ReasoningParam{
			Effort:  shared.ReasoningEffort(reasoningEffort),
			Summary: shared.ReasoningSummaryConcise,
		}
	}

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

	if item.OfShellCall != nil {
		action := item.OfShellCall.Action
		content := formatJSONLog(action)
		return LoggingBox{
			Title:   fmt.Sprintf("%s: shell_call", titlePrefix),
			Content: content,
			Color:   2,
		}
	}

	if item.OfShellCallOutput != nil {
		content := formatJSONLog(item.OfShellCallOutput.Output)
		return LoggingBox{
			Title:   fmt.Sprintf("%s: shell_call_output", titlePrefix),
			Content: content,
			Color:   2,
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

func logCompletionRequest(request *util.CompletionRequest, inputItems responses.ResponseInputParam, tools []responses.ToolUnionParam, reasoningEffort string) {
	reasoning := "(default)"
	if reasoningEffort != "" {
		reasoning = reasoningEffort
	}
	serviceTier := "(default)"
	if normalized := normalizeServiceTier(request.ServiceTier); normalized != "" {
		serviceTier = normalized
	}

	box := LoggingBox{
		Title: "LLM Request",
		Content: fmt.Sprintf("model: %s\nmax_tokens: %d\nreasoning_effort: %s\nservice_tier: %s",
			request.Model,
			request.MaxTokens,
			reasoning,
			serviceTier,
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

func shellCallsFromOutputItems(items []responses.ResponseOutputItemUnion) []*util.ShellCall {
	var shellCalls []*util.ShellCall
	for _, item := range items {
		if item.Type != "shell_call" {
			continue
		}
		call := item.AsShellCall()
		shellCalls = append(shellCalls, &util.ShellCall{
			CallID:          call.CallID,
			Commands:        call.Action.Commands,
			TimeoutMs:       call.Action.TimeoutMs,
			MaxOutputLength: call.Action.MaxOutputLength,
		})
	}
	return shellCalls
}

func finalizeCompletionResponse(completion string, toolCalls []*util.ToolCall, shellCalls []*util.ShellCall) *util.CompletionResponse {
	resp := &util.CompletionResponse{
		Completion: completion,
		ToolCalls:  toolCalls,
		ShellCalls: shellCalls,
	}
	if len(toolCalls) > 0 {
		resp.FunctionName = toolCalls[0].Function.Name
		resp.FunctionParameters = toolCalls[0].Function.Parameters
	}
	return resp
}

func recordShellCall(shellCallMap map[string]*util.ShellCall, shellCallOrder *[]string, call *util.ShellCall) {
	if call == nil || call.CallID == "" {
		return
	}

	existing, ok := shellCallMap[call.CallID]
	if !ok {
		shellCallMap[call.CallID] = call
		*shellCallOrder = append(*shellCallOrder, call.CallID)
		return
	}

	if len(call.Commands) > 0 {
		existing.Commands = call.Commands
	}
	if call.TimeoutMs != 0 {
		existing.TimeoutMs = call.TimeoutMs
	}
	if call.MaxOutputLength != 0 {
		existing.MaxOutputLength = call.MaxOutputLength
	}
}

func mergeShellCallsFromOutput(shellCallMap map[string]*util.ShellCall, shellCallOrder *[]string, output []responses.ResponseOutputItemUnion) {
	for _, item := range output {
		if item.Type != "shell_call" {
			continue
		}
		call := item.AsShellCall()
		recordShellCall(shellCallMap, shellCallOrder, &util.ShellCall{
			CallID:          call.CallID,
			Commands:        call.Action.Commands,
			TimeoutMs:       call.Action.TimeoutMs,
			MaxOutputLength: call.Action.MaxOutputLength,
		})
	}
}

func (this *OpenAIClient) Completion(request *util.CompletionRequest) (*util.CompletionResponse, error) {
	reasoningEffort := strings.TrimSpace(request.ReasoningEffort)
	if reasoningEffort != "" && this.isReasoningUnsupportedForModel(request.Model) {
		if request.Verbose {
			log.Printf("Reasoning disabled for model %s (requested effort=%q): model is cached as reasoning-unsupported.", request.Model, reasoningEffort)
		}
		reasoningEffort = ""
	}
	serviceTier := normalizeServiceTier(request.ServiceTier)
	if serviceTier != "" && this.isServiceTierUnsupported(serviceTier) {
		if request.Verbose {
			log.Printf("Service tier disabled (requested tier=%q): tier is cached as unsupported.", serviceTier)
		}
		serviceTier = ""
	}

	effectiveRequest := *request
	effectiveRequest.ServiceTier = serviceTier
	params := buildResponseParams(&effectiveRequest, reasoningEffort)
	if request.Verbose {
		inputItems := buildInputItems(request)
		tools := buildTools(request.Functions, request.Tools)
		logCompletionRequest(&effectiveRequest, inputItems, tools, reasoningEffort)
	}
	var response *responses.Response

	err := withExponentialBackoff(func() error {
		var innerErr error
		response, innerErr = this.client.Responses.New(request.Ctx, params)
		return innerErr
	})
	if err != nil && serviceTier != "" && isServiceTierUnsupportedError(err) {
		log.Printf("Responses API rejected service_tier=%q: %v", serviceTier, err)
		this.markServiceTierUnsupported(serviceTier)
		log.Printf("Retrying without service_tier.")

		serviceTier = ""
		effectiveRequest.ServiceTier = ""
		params = buildResponseParams(&effectiveRequest, reasoningEffort)
		err = withExponentialBackoff(func() error {
			var innerErr error
			response, innerErr = this.client.Responses.New(request.Ctx, params)
			return innerErr
		})
		if err == nil {
			log.Printf("Retry without service_tier succeeded.")
		}
	}
	if err != nil && reasoningEffort != "" && isReasoningUnsupportedError(err) {
		log.Printf("Model %s rejected reasoning parameter (effort=%q): %v", request.Model, reasoningEffort, err)
		this.markReasoningUnsupportedForModel(request.Model)
		log.Printf("Retrying model %s without reasoning parameter.", request.Model)

		params = buildResponseParams(&effectiveRequest, "")
		err = withExponentialBackoff(func() error {
			var innerErr error
			response, innerErr = this.client.Responses.New(request.Ctx, params)
			return innerErr
		})
		if err == nil {
			log.Printf("Retry without reasoning succeeded for model %s.", request.Model)
		}
	}
	if err != nil {
		if strings.Contains(err.Error(), ERR_429) {
			return nil, errors.New(ERR_429_HELP)
		}
		return nil, err
	}

	toolCalls := toolCallsFromOutputItems(response.Output)
	shellCalls := shellCallsFromOutputItems(response.Output)
	final := finalizeCompletionResponse(response.OutputText(), toolCalls, shellCalls)
	incompleteErr := responseIncompleteError(response)
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
		if len(shellCalls) > 0 {
			box.Children = append(box.Children, LoggingBox{
				Title:   "Shell Calls",
				Content: formatJSONLog(shellCalls),
				Color:   1,
			})
		}
		PrintLoggingBox(box)
	} else if response != nil && response.ID != "" {
		log.Printf("LLM response id: %s", response.ID)
	}
	return final, incompleteErr
}

type streamToolCallInfo struct {
	CallID    string
	Name      string
	Arguments strings.Builder
}

type reasoningSummaryPrinter struct {
	writer              io.Writer
	active              bool
	currentEndsWithLine bool
}

func newReasoningSummaryPrinter(writer io.Writer) *reasoningSummaryPrinter {
	return &reasoningSummaryPrinter{writer: writer}
}

func (p *reasoningSummaryPrinter) WriteDelta(delta string) {
	if delta == "" {
		return
	}

	if !p.active {
		p.writeString("\nReasoning: ")
		p.active = true
		p.currentEndsWithLine = false
	}

	p.writeString(delta)
	p.currentEndsWithLine = strings.HasSuffix(delta, "\n")
}

func (p *reasoningSummaryPrinter) FinishSummary() {
	if !p.active {
		return
	}
	if !p.currentEndsWithLine {
		p.writeString("\n")
	}
	p.active = false
	p.currentEndsWithLine = false
}

func (p *reasoningSummaryPrinter) BeforeOutput() {
	p.FinishSummary()
}

func (p *reasoningSummaryPrinter) writeString(s string) {
	_, _ = p.writer.Write([]byte(s))
}

func responseIncompleteError(response *responses.Response) error {
	if response == nil {
		return nil
	}
	if response.Status != responses.ResponseStatusIncomplete {
		return nil
	}

	switch response.IncompleteDetails.Reason {
	case "max_output_tokens":
		if response.MaxOutputTokens > 0 {
			return fmt.Errorf("Response hit max_output_tokens (%d). The output above may be incomplete.", response.MaxOutputTokens)
		}
		return fmt.Errorf("Response hit the model output token limit. The output above may be incomplete.")
	default:
		return nil
	}
}

func (this *OpenAIClient) CompletionStream(request *util.CompletionRequest, writer io.Writer) (*util.CompletionResponse, error) {
	reasoningEffort := strings.TrimSpace(request.ReasoningEffort)
	if reasoningEffort != "" && this.isReasoningUnsupportedForModel(request.Model) {
		if request.Verbose {
			log.Printf("Reasoning disabled for model %s (requested effort=%q): model is cached as reasoning-unsupported.", request.Model, reasoningEffort)
		}
		reasoningEffort = ""
	}
	serviceTier := normalizeServiceTier(request.ServiceTier)
	if serviceTier != "" && this.isServiceTierUnsupported(serviceTier) {
		if request.Verbose {
			log.Printf("Service tier disabled (requested tier=%q): tier is cached as unsupported.", serviceTier)
		}
		serviceTier = ""
	}

	effectiveRequest := *request
	effectiveRequest.ServiceTier = serviceTier
	params := buildResponseParams(&effectiveRequest, reasoningEffort)
	if request.Verbose {
		inputItems := buildInputItems(request)
		tools := buildTools(request.Functions, request.Tools)
		logCompletionRequest(&effectiveRequest, inputItems, tools, reasoningEffort)
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
	if err != nil && serviceTier != "" && isServiceTierUnsupportedError(err) {
		log.Printf("Responses API rejected service_tier=%q for streaming request: %v", serviceTier, err)
		this.markServiceTierUnsupported(serviceTier)
		log.Printf("Retrying streaming request without service_tier.")
		if stream != nil {
			stream.Close()
		}

		serviceTier = ""
		effectiveRequest.ServiceTier = ""
		params = buildResponseParams(&effectiveRequest, reasoningEffort)
		err = withExponentialBackoff(func() error {
			stream = this.client.Responses.NewStreaming(innerCtx, params)
			innerErr = stream.Err()
			return innerErr
		})
		if err == nil {
			log.Printf("Streaming retry without service_tier succeeded.")
		}
	}
	if err != nil && reasoningEffort != "" && isReasoningUnsupportedError(err) {
		log.Printf("Model %s rejected reasoning parameter for streaming request (effort=%q): %v", request.Model, reasoningEffort, err)
		this.markReasoningUnsupportedForModel(request.Model)
		log.Printf("Retrying streaming request for model %s without reasoning parameter.", request.Model)
		if stream != nil {
			stream.Close()
		}

		params = buildResponseParams(&effectiveRequest, "")
		err = withExponentialBackoff(func() error {
			stream = this.client.Responses.NewStreaming(innerCtx, params)
			innerErr = stream.Err()
			return innerErr
		})
		if err == nil {
			log.Printf("Streaming retry without reasoning succeeded for model %s.", request.Model)
		}
	}
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
	shellCallMap := map[string]*util.ShellCall{}
	shellCallOrder := []string{}
	reasoningPrinter := newReasoningSummaryPrinter(writer)

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
		case "response.incomplete":
			incomplete := event.AsResponseIncomplete()
			if responseID == "" {
				responseID = incomplete.Response.ID
			}
			completedResponse = &incomplete.Response
		case "response.output_text.delta":
			delta := event.AsResponseOutputTextDelta()
			if delta.Delta != "" {
				reasoningPrinter.BeforeOutput()
				writer.Write([]byte(delta.Delta))
				completion.WriteString(delta.Delta)
			}
		case "response.reasoning_summary_text.delta":
			delta := event.AsResponseReasoningSummaryTextDelta()
			reasoningPrinter.WriteDelta(delta.Delta)
		case "response.reasoning_summary_text.done":
			reasoningPrinter.FinishSummary()
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
			} else if added.Item.Type == "shell_call" {
				call := added.Item.AsShellCall()
				recordShellCall(shellCallMap, &shellCallOrder, &util.ShellCall{
					CallID:          call.CallID,
					Commands:        call.Action.Commands,
					TimeoutMs:       call.Action.TimeoutMs,
					MaxOutputLength: call.Action.MaxOutputLength,
				})
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
			} else if done.Item.Type == "shell_call" {
				call := done.Item.AsShellCall()
				recordShellCall(shellCallMap, &shellCallOrder, &util.ShellCall{
					CallID:          call.CallID,
					Commands:        call.Action.Commands,
					TimeoutMs:       call.Action.TimeoutMs,
					MaxOutputLength: call.Action.MaxOutputLength,
				})
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
	if completedResponse != nil {
		mergeShellCallsFromOutput(shellCallMap, &shellCallOrder, completedResponse.Output)
	}
	shellCallResults := make([]*util.ShellCall, 0, len(shellCallOrder))
	for _, id := range shellCallOrder {
		call, ok := shellCallMap[id]
		if !ok {
			continue
		}
		shellCallResults = append(shellCallResults, call)
	}

	final := finalizeCompletionResponse(completion.String(), toolCallResults, shellCallResults)
	incompleteErr := responseIncompleteError(completedResponse)
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
		if len(shellCallResults) > 0 {
			box.Children = append(box.Children, LoggingBox{
				Title:   "Shell Calls",
				Content: formatJSONLog(shellCallResults),
				Color:   1,
			})
		}
		PrintLoggingBox(box)
	} else if responseID != "" {
		log.Printf("LLM response id: %s", responseID)
	}
	return final, incompleteErr
}
