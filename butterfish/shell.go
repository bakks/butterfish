package butterfish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/bakks/butterfish/prompt"
	"github.com/bakks/butterfish/util"

	"github.com/bakks/tiktoken-go"
	"github.com/mitchellh/go-ps"
	"golang.org/x/term"
)

// The default models to use for autosuggest and prompt generation
// These are the models that are used if we can't find the given model
// in Tiktoken
// These models are used specifically for counting tokens to pack into
// the prompt context
const DEFAULT_AUTOSUGGEST_ENCODER = tiktoken.MODEL_CL100K_BASE
const DEFAULT_PROMPT_ENCODER = tiktoken.MODEL_CL100K_BASE
const defaultShellMaxPromptTokens = 16384
const gpt5ShellMaxPromptTokens = 65536

const ESC_CUP = "\x1b[6n" // Request the cursor position
const ESC_UP = "\x1b[%dA"
const ESC_RIGHT = "\x1b[%dC"
const ESC_LEFT = "\x1b[%dD"
const ESC_CLEAR = "\x1b[0K"
const CLEAR_COLOR = "\x1b[0m"

// Special characters that we wrap the shell's command prompt in (PS1) so
// that we can detect where it starts and ends.
const PROMPT_PREFIX = "\033Q"
const PROMPT_SUFFIX = "\033R"
const PROMPT_PREFIX_ESCAPED = "\\033Q"
const PROMPT_SUFFIX_ESCAPED = "\\033R"
const EMOJI_DEFAULT = "🐠"
const EMOJI_GOAL = "🟦"
const EMOJI_GOAL_UNSAFE = "⚡"

var ps1Regex = regexp.MustCompile(" ([0-9]+)" + PROMPT_SUFFIX)
var ps1FullRegex = regexp.MustCompile(EMOJI_DEFAULT + " ([0-9]+)" + PROMPT_SUFFIX)

var DarkShellColorScheme = &ShellColorScheme{
	Prompt:           "\x1b[38;5;154m",
	PromptGoal:       "\x1b[38;5;200m",
	PromptGoalUnsafe: "\x1b[38;5;9m",
	Command:          "\x1b[0m",
	Autosuggest:      "\x1b[38;5;241m",
	Answer:           "\x1b[38;5;221m", // yellow
	AnswerHighlight:  "\x1b[38;5;204m", // orange
	GoalMode:         "\x1b[38;5;51m",
	Error:            "\x1b[38;5;196m",
}

var LightShellColorScheme = &ShellColorScheme{
	Prompt:           "\x1b[38;5;28m",
	PromptGoal:       "\x1b[38;5;200m",
	PromptGoalUnsafe: "\x1b[38;5;9m",
	Command:          "\x1b[0m",
	Autosuggest:      "\x1b[38;5;241m",
	Answer:           "\x1b[38;5;18m", // Dark blue
	AnswerHighlight:  "\x1b[38;5;6m",
	GoalMode:         "\x1b[38;5;18m",
	Error:            "\x1b[38;5;196m",
}

func RunShell(ctx context.Context, config *ButterfishConfig) error {
	envVars := []string{"BUTTERFISH_SHELL=1"}

	ptmx, ptyCleanup, err := ptyCommand(ctx, envVars, []string{config.ShellBinary})
	if err != nil {
		return err
	}
	defer ptyCleanup()

	bf, err := NewButterfish(ctx, config)
	if err != nil {
		return err
	}
	//fmt.Println("Starting butterfish shell")

	bf.ShellMultiplexer(ptmx, ptmx, os.Stdin, os.Stdout)
	return nil
}

const (
	historyTypePrompt = iota
	historyTypeShellInput
	historyTypeShellOutput
	historyTypeLLMOutput
	historyTypeFunctionOutput
	historyTypeToolOutput
)

// Turn history type enum to a string
func HistoryTypeToString(historyType int) string {
	switch historyType {
	case historyTypePrompt:
		return "Prompt"
	case historyTypeShellInput:
		return "Shell Input"
	case historyTypeShellOutput:
		return "Shell Output"
	case historyTypeLLMOutput:
		return "LLM Output"
	case historyTypeFunctionOutput:
		return "Function Output"
	default:
		return "Unknown"
	}
}

func ShellHistoryTypeToRole(historyType int) string {
	switch historyType {
	case historyTypeLLMOutput:
		return "assistant"
	default:
		return "user"
	}
}

func shellPromptWindowForModel(model string, configuredMax int) int {
	effectiveMax := configuredMax
	modelLower := strings.ToLower(strings.TrimSpace(model))
	if configuredMax == defaultShellMaxPromptTokens && strings.HasPrefix(modelLower, "gpt-5") {
		effectiveMax = gpt5ShellMaxPromptTokens
	}

	return min(NumTokensForModel(model), effectiveMax)
}

type Tokenization struct {
	InputLength int    // the unprocessed length of the pretokenized plus truncated content
	NumTokens   int    // number of tokens in the data
	Data        string // tokenized and truncated content
}

// HistoryBuffer keeps a content buffer, plus an enum of the type of content
// (user prompt, shell output, etc), plus a cache of tokenizations of the
// content. Tokenizations are cached for specific encodings, for example
// newer models use a different encoding than older models.
type HistoryBuffer struct {
	Type            int
	Content         *ShellBuffer
	FunctionName    string
	FunctionParams  string
	ToolCallID      string
	ToolType        string
	ShellCall       *util.ShellCall
	ShellCallOutput *util.ShellCallOutput

	// This is to cache tokenization plus truncation of the content
	// It maps from encoding name to the tokenization of the output
	Tokenizations map[string]Tokenization
}

func (this *HistoryBuffer) SetTokenization(encoding string, inputLength int, numTokens int, data string) {
	if this.Tokenizations == nil {
		this.Tokenizations = make(map[string]Tokenization)
	}
	this.Tokenizations[encoding] = Tokenization{
		InputLength: inputLength,
		NumTokens:   numTokens,
		Data:        data,
	}
}

func (this *HistoryBuffer) GetTokenization(encoding string, length int) (string, int, bool) {
	if this.Tokenizations == nil {
		this.Tokenizations = make(map[string]Tokenization)
	}

	tokenization, ok := this.Tokenizations[encoding]
	if !ok {
		return "", 0, false
	}
	if tokenization.InputLength != length {
		return "", 0, false
	}
	return tokenization.Data, tokenization.NumTokens, true
}

// ShellHistory keeps a record of past shell history and LLM interaction in
// a slice of HistoryBuffer objects. You can add a new block, append to
// the last block, and get the the last n bytes of the history as an array of
// HistoryBlocks.
type ShellHistory struct {
	Blocks []*HistoryBuffer
	mutex  sync.Mutex
}

func NewShellHistory() *ShellHistory {
	return &ShellHistory{
		Blocks: make([]*HistoryBuffer, 0),
	}
}

func (this *ShellHistory) add(historyType int, block string) {
	buffer := NewShellBuffer()
	buffer.WriteNoRender(block)
	this.Blocks = append(this.Blocks, &HistoryBuffer{
		Type:    historyType,
		Content: buffer,
	})
}

func (this *ShellHistory) Append(historyType int, data string) {
	// if data is empty, we don't want to add a new block
	if len(data) == 0 {
		return
	}

	this.mutex.Lock()
	defer this.mutex.Unlock()

	numBlocks := len(this.Blocks)
	// if we have a block already, and it matches the type, append to it
	if numBlocks > 0 {
		lastBlock := this.Blocks[numBlocks-1]

		if lastBlock.Type == historyType {
			lastBlock.Content.WriteNoRender(data)
			return
		}
	}

	// if the history type doesn't match we fall through and add a new block
	this.add(historyType, data)
}

func (this *ShellHistory) AddFunctionCall(name, params, callID string) {
	this.mutex.Lock()
	defer this.mutex.Unlock()

	this.Blocks = append(this.Blocks, &HistoryBuffer{
		Type:           historyTypeLLMOutput,
		FunctionName:   name,
		FunctionParams: params,
		ToolCallID:     callID,
		Content:        NewShellBuffer(),
	})
}

func (this *ShellHistory) AddShellCall(call *util.ShellCall) {
	this.mutex.Lock()
	defer this.mutex.Unlock()

	if call == nil {
		return
	}

	this.Blocks = append(this.Blocks, &HistoryBuffer{
		Type:           historyTypeLLMOutput,
		ToolCallID:     call.CallID,
		ToolType:       "shell",
		ShellCall:      call,
		Content:        NewShellBuffer(),
		FunctionName:   "",
		FunctionParams: "",
	})
}

func (this *ShellHistory) AppendFunctionOutput(name, callID, data string) {
	this.mutex.Lock()
	defer this.mutex.Unlock()

	// if data is empty, we don't want to add a new block
	if len(data) == 0 {
		return
	}

	numBlocks := len(this.Blocks)
	var lastBlock *HistoryBuffer
	// if we have a block already, and it matches the type, append to it
	if numBlocks > 0 {
		lastBlock = this.Blocks[numBlocks-1]
		if lastBlock.Type == historyTypeFunctionOutput && lastBlock.FunctionName == name && lastBlock.ToolCallID == callID {
			lastBlock.Content.WriteNoRender(data)
			return
		}
	}

	// if the history type doesn't match we fall through and add a new block
	this.add(historyTypeFunctionOutput, data)
	lastBlock = this.Blocks[numBlocks]
	lastBlock.FunctionName = name
	lastBlock.ToolCallID = callID
}

func (this *ShellHistory) AppendFunctionOutputAllowEmpty(name, callID string) {
	this.mutex.Lock()
	defer this.mutex.Unlock()

	numBlocks := len(this.Blocks)
	if numBlocks > 0 {
		lastBlock := this.Blocks[numBlocks-1]
		if lastBlock.Type == historyTypeFunctionOutput && lastBlock.FunctionName == name && lastBlock.ToolCallID == callID {
			return
		}
	}

	this.add(historyTypeFunctionOutput, "")
	lastBlock := this.Blocks[len(this.Blocks)-1]
	lastBlock.FunctionName = name
	lastBlock.ToolCallID = callID
}

func (this *ShellHistory) AppendShellCallOutput(output *util.ShellCallOutput) {
	this.mutex.Lock()
	defer this.mutex.Unlock()

	if output == nil {
		return
	}

	combined := strings.Builder{}
	for _, item := range output.Output {
		if item.Stdout != "" {
			combined.WriteString(item.Stdout)
		}
		if item.Stderr != "" {
			if combined.Len() > 0 && !strings.HasSuffix(combined.String(), "\n") {
				combined.WriteString("\n")
			}
			combined.WriteString(item.Stderr)
		}
	}

	data := combined.String()
	if data == "" {
		data = "(no output)"
	}

	this.add(historyTypeToolOutput, data)
	lastBlock := this.Blocks[len(this.Blocks)-1]
	lastBlock.ToolCallID = output.CallID
	lastBlock.ToolType = "shell"
	lastBlock.ShellCallOutput = output
}

// Go back in history for a certain number of bytes.
func (this *ShellHistory) GetLastNBytes(numBytes int, truncateLength int) []util.HistoryBlock {
	this.mutex.Lock()
	defer this.mutex.Unlock()

	var blocks []util.HistoryBlock

	for i := len(this.Blocks) - 1; i >= 0 && numBytes > 0; i-- {
		block := this.Blocks[i]
		content := sanitizeTTYString(block.Content.String())
		if len(content) > truncateLength {
			content = content[:truncateLength]
		}
		if len(content) > numBytes {
			break // we don't want a weird partial line so we bail out here
		}
		blocks = append(blocks, util.HistoryBlock{
			Type:       block.Type,
			Content:    content,
			ToolCallId: block.ToolCallID,
		})
		numBytes -= len(content)
	}

	// reverse the blocks slice
	for i := len(blocks)/2 - 1; i >= 0; i-- {
		opp := len(blocks) - 1 - i
		blocks[i], blocks[opp] = blocks[opp], blocks[i]
	}

	return blocks
}

func (this *ShellHistory) IterateBlocks(cb func(block *HistoryBuffer) bool) {
	this.mutex.Lock()
	defer this.mutex.Unlock()

	for i := len(this.Blocks) - 1; i >= 0; i-- {
		cont := cb(this.Blocks[i])
		if !cont {
			break
		}
	}
}

// This is not thread safe
func (this *ShellHistory) LogRecentHistory() {
	blocks := this.GetLastNBytes(2000, 512)
	log.Printf("Recent history: =======================================")
	builder := strings.Builder{}
	for _, block := range blocks {
		builder.WriteString(fmt.Sprintf("%s: %s\n", HistoryTypeToString(block.Type), block.Content))
	}
	log.Print(builder.String())
	log.Printf("=======================================")
}

func HistoryBlocksToString(blocks []util.HistoryBlock) string {
	var sb strings.Builder
	for i, block := range blocks {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(block.Content)
	}
	return sb.String()
}

const (
	stateNormal = iota
	stateShell
	statePrompting
	statePromptResponse
)

var stateNames = []string{
	"Normal",
	"Shell",
	"Prompting",
	"PromptResponse",
}

type AutosuggestResult struct {
	Command    string
	Suggestion string
}

type ShellColorScheme struct {
	Prompt           string
	PromptGoal       string
	PromptGoalUnsafe string
	Error            string
	Command          string
	Autosuggest      string
	Answer           string
	AnswerHighlight  string
	GoalMode         string
}

type ShellState struct {
	Butterfish *ButterfishCtx
	ParentOut  io.Writer
	ChildIn    io.Writer
	Sigwinch   chan os.Signal

	// set based on model
	PromptMaxTokens      int
	AutosuggestMaxTokens int

	// The current state of the shell
	State                      int
	GoalMode                   bool
	GoalModeBuffer             string
	GoalModeGoal               string
	GoalModeUnsafe             bool
	ActiveFunction             string
	ActiveFunctionCallID       string
	ActiveShellMaxOutputLength int64
	PromptSuffixCounter        int
	ChildOutReader             chan *byteMsg
	ParentInReader             chan *byteMsg
	CursorPosChan              chan *cursorPosition
	PromptOutputChan           chan *util.CompletionResponse
	PrintErrorChan             chan error
	AutosuggestChan            chan *AutosuggestResult
	History                    *ShellHistory
	PromptAnswerWriter         io.Writer
	PromptGoalAnswerWriter     io.Writer
	StyleWriter                *util.StyleCodeblocksWriter
	Prompt                     *ShellBuffer
	PromptResponseCancel       context.CancelFunc
	Command                    *ShellBuffer
	TerminalWidth              int
	Color                      *ShellColorScheme
	LastTabPassthrough         time.Time
	parentInBuffer             []byte
	// these are used to estimate number of tokens
	AutosuggestEncoder *tiktoken.Tiktoken
	PromptEncoder      *tiktoken.Tiktoken

	// autosuggest config
	AutosuggestEnabled bool
	LastAutosuggest    string
	AutosuggestCtx     context.Context
	AutosuggestCancel  context.CancelFunc
	AutosuggestBuffer  *ShellBuffer

	// Track whether we should bypass output parsing/history for an active TUI app.
	InteractiveChildPassthrough  bool
	RunningChildren              bool
	RunningChildrenLastCheck     time.Time
	RunningChildrenCheckInterval time.Duration
	HasRunningChildrenFn         func() bool
	TUITailBuffer                []byte
	TUITailMaxBytes              int
}

func (this *ShellState) setState(state int) {
	if this.State == state {
		return
	}

	if this.Butterfish.Config.Verbose > 1 {
		log.Printf("State change: %s -> %s", stateNames[this.State], stateNames[state])
	}

	this.State = state
}

// Cache the expensive child-process scan for a short interval so typing in
// stateNormal does not trigger ps.Processes() calls on every keystroke.
func (this *ShellState) hasRunningChildrenCached(force bool) bool {
	interval := this.RunningChildrenCheckInterval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}

	now := time.Now()
	needRefresh := force ||
		this.RunningChildrenLastCheck.IsZero() ||
		now.Sub(this.RunningChildrenLastCheck) >= interval
	if needRefresh {
		checkFn := this.HasRunningChildrenFn
		if checkFn == nil {
			checkFn = HasRunningChildren
		}
		this.RunningChildren = checkFn()
		this.RunningChildrenLastCheck = now
		if !this.RunningChildren && this.InteractiveChildPassthrough {
			this.InteractiveChildPassthrough = false
			this.flushTUITailToHistory()
		}
	}

	return this.RunningChildren
}

// Keep only human-useful plain text from TUI output before adding to the tail.
// We strip escape/control traffic because TUI redraw streams are mostly cursor
// movement bytes and can dominate history size/cost.
func sanitizeTUITailData(data []byte) []byte {
	out := make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		b := data[i]

		if b == 0x1b {
			// Skip CSI sequence: ESC [ ... final-byte
			if i+1 < len(data) && data[i+1] == '[' {
				i += 2
				for i < len(data) {
					if data[i] >= 0x40 && data[i] <= 0x7e {
						i++
						break
					}
					i++
				}
				continue
			}

			// Skip OSC sequence: ESC ] ... BEL or ESC \
			if i+1 < len(data) && data[i+1] == ']' {
				i += 2
				for i < len(data) {
					if data[i] == 0x07 {
						i++
						break
					}
					if data[i] == 0x1b && i+1 < len(data) && data[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
				continue
			}

			i++
			continue
		}

		switch b {
		case '\n', '\t':
			out = append(out, b)
		case '\r':
			// ignore carriage returns
		default:
			if b >= 0x20 && b <= 0x7e {
				out = append(out, b)
			}
		}
		i++
	}

	return out
}

// Append sanitized interactive output and keep only the last TUITailMaxBytes.
func (this *ShellState) appendTUITail(data []byte) {
	if this.TUITailMaxBytes <= 0 {
		return
	}

	cleaned := sanitizeTUITailData(data)
	if len(cleaned) == 0 {
		return
	}

	if len(cleaned) >= this.TUITailMaxBytes {
		this.TUITailBuffer = append([]byte{}, cleaned[len(cleaned)-this.TUITailMaxBytes:]...)
		return
	}

	next := make([]byte, 0, len(this.TUITailBuffer)+len(cleaned))
	next = append(next, this.TUITailBuffer...)
	next = append(next, cleaned...)

	if len(next) > this.TUITailMaxBytes {
		next = next[len(next)-this.TUITailMaxBytes:]
	}

	this.TUITailBuffer = next
}

// Flush a bounded interactive tail snapshot to history when the TUI session
// ends, so prompts still get some recent context without unbounded redraw spam.
func (this *ShellState) flushTUITailToHistory() {
	if len(this.TUITailBuffer) == 0 {
		return
	}

	trimmed := strings.TrimSpace(string(this.TUITailBuffer))
	this.TUITailBuffer = nil
	if trimmed == "" {
		return
	}

	text := fmt.Sprintf("\n[interactive session tail]\n%s\n", trimmed)
	this.History.Append(historyTypeShellOutput, text)
}

func clearByteChan(r <-chan *byteMsg, timeout time.Duration) {
	// then wait for timeout
	target := 2
	seen := 0

	for {
		select {
		case <-time.After(timeout):
			return
		case msg := <-r:
			// if msg.Data includes \n we break
			if bytes.Contains(msg.Data, []byte("\n")) {
				seen++
				if seen >= target {
					return
				}
			}
			continue
		}
	}
}

func (this *ShellState) GetCursorPosition() (int, int) {
	// send the cursor position request
	this.ParentOut.Write([]byte(ESC_CUP))
	// we wait 5s, if we haven't gotten a response by then we likely have a bug
	timeout := time.After(5000 * time.Millisecond)
	var pos *cursorPosition

	// the parent in reader watches for these responses, set timeout and
	// panic if we don't get a response
	select {
	case <-timeout:
		panic(`Timeout waiting for cursor position response, this means that either:
- Butterfish has frozen due to a bug.
- You're using a terminal emulator that doesn't work well with butterfish.
Please submit an issue to https://github.com/bakks/butterfish.`)

	case pos = <-this.CursorPosChan:
	}

	// it's possible that we have a stale response, so we loop on the channel
	// until we get the most recent one
	for {
		select {
		case pos = <-this.CursorPosChan:
			continue
		default:
			return pos.Row, pos.Column
		}
	}
}

// This sets the PS1 shell variable, which is the prompt that the shell
// displays before each command.
// We need to be able to parse the child shell's prompt to determine where
// it starts, ends, exit code, and allow customization to show the user that
// we're inside butterfish shell. The PS1 is roughly the following:
// PS1 := promptPrefix $PS1 ShellCommandPrompt $? promptSuffix
func (this *ButterfishCtx) SetPS1(childIn io.Writer) {
	shell := this.Config.ParseShell()
	var ps1 string

	switch shell {
	case "bash", "sh":
		// the \[ and \] are bash-specific and tell bash to not count the enclosed
		// characters when calculating the cursor position
		ps1 = "PS1=$'\\[%s\\]'$PS1$'%s\\[ $?%s\\] '\n"
	case "zsh":
		// the %%{ and %%} are zsh-specific and tell zsh to not count the enclosed
		// characters when calculating the cursor position
		ps1 = "PS1=$'%%{%s%%}'$PS1$'%s%%{ %%?%s%%} '\n"
	default:
		log.Printf("Unknown shell %s, Butterfish is going to leave the PS1 alone. This means that you won't get a custom prompt in Butterfish, and Butterfish won't be able to parse the exit code of the previous command, used for certain features. Create an issue at https://github.com/bakks/butterfish.", shell)
		return
	}

	promptIcon := ""
	if !this.Config.ShellLeavePromptAlone {
		promptIcon = EMOJI_DEFAULT
	}

	fmt.Fprintf(childIn,
		ps1,
		PROMPT_PREFIX_ESCAPED,
		promptIcon,
		PROMPT_SUFFIX_ESCAPED)
}

// Given a string of terminal output, identify terminal prompts based on the
// custom PS1 escape sequences we set.
// Returns:
//   - The last exit code/status seen in the string (i.e. will be non-zero if
//     previous command failed.
//   - The number of prompts identified in the string.
//   - The string with the special prompt escape sequences removed.
func ParsePS1(data string, regex *regexp.Regexp, currIcon string) (int, int, string) {
	matches := regex.FindAllStringSubmatch(data, -1)

	if len(matches) == 0 {
		return 0, 0, data
	}

	lastStatus := 0
	prompts := 0

	for _, match := range matches {
		var err error
		lastStatus, err = strconv.Atoi(match[1])
		if err != nil {
			log.Printf("Error parsing PS1 match: %s", err)
		}
		prompts++
	}

	// Remove matches of suffix
	cleaned := regex.ReplaceAllString(data, currIcon)
	// Remove the prefix
	cleaned = strings.ReplaceAll(cleaned, PROMPT_PREFIX, "")

	return lastStatus, prompts, cleaned
}

func (this *ShellState) ParsePS1(data string) (int, int, string) {
	var regex *regexp.Regexp
	if this.Butterfish.Config.ShellLeavePromptAlone {
		regex = ps1Regex
	} else {
		regex = ps1FullRegex
	}

	currIcon := ""
	if !this.Butterfish.Config.ShellLeavePromptAlone {
		if this.GoalMode {
			if this.GoalModeUnsafe {
				currIcon = EMOJI_GOAL_UNSAFE
			} else {
				currIcon = EMOJI_GOAL
			}
		} else {
			currIcon = EMOJI_DEFAULT
		}
	}

	return ParsePS1(data, regex, currIcon)
}

// zsh appears to use this sequence to clear formatting and the rest of the line
// before printing a prompt
var ZSH_CLEAR_REGEX = regexp.MustCompile("^\x1b\\[1m\x1b\\[3m%\x1b\\[23m\x1b\\[1m\x1b\\[0m\x20+\x0d\x20\x0d")

func (this *ShellState) FilterChildOut(data string) bool {
	if len(data) > 0 && strings.HasPrefix(data, "\x1b[1m") && ZSH_CLEAR_REGEX.MatchString(data) {
		return true
	}

	return false
}

// Heuristic: detect CSI sequences typically used by interactive TUI redraws.
// We intentionally ignore SGR ('m') color-only sequences common in normal output.
func likelyTUIControlSequence(data []byte) bool {
	for i := 0; i+2 < len(data); i++ {
		if data[i] != 0x1b || data[i+1] != '[' {
			continue
		}

		for j := i + 2; j < len(data); j++ {
			b := data[j]
			if b < 0x40 || b > 0x7e {
				continue
			}

			switch b {
			case 'm':
				// style/color only
			case 'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'J', 'K', 'S', 'T', 'L', 'M', 'P', 'X', 'd', 'f', 'h', 'l', 'r', 's', 'u':
				return true
			}
			break
		}
	}

	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (this *ButterfishCtx) ShellMultiplexer(
	childIn io.Writer, childOut io.Reader,
	parentIn io.Reader, parentOut io.Writer) {

	this.SetPS1(childIn)

	colorScheme := DarkShellColorScheme
	if !this.Config.ColorDark {
		colorScheme = LightShellColorScheme
	}

	log.Printf("Starting shell multiplexer")

	childOutReader := make(chan *byteMsg, 8)
	parentInReader := make(chan *byteMsg, 8)
	// This is a buffered channel so that we don't block reading input when
	// pushing a new position
	parentPositionChan := make(chan *cursorPosition, 128)

	termWidth, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		panic(err)
	}

	carriageReturnWriter := util.NewReplaceWriter(parentOut, "\n", "\r\n")
	codeblocksColorScheme := "monokai"
	if !this.Config.ColorDark {
		codeblocksColorScheme = "monokailight"
	}
	styleCodeblocksWriter := util.NewStyleCodeblocksWriter(
		carriageReturnWriter,
		termWidth,
		colorScheme.Answer,
		colorScheme.AnswerHighlight,
		codeblocksColorScheme)
	styleCodeblocksWriterGoal := util.NewStyleCodeblocksWriter(
		carriageReturnWriter,
		termWidth,
		colorScheme.GoalMode,
		colorScheme.AnswerHighlight,
		codeblocksColorScheme)

	sigwinch := make(chan os.Signal, 1)
	signal.Notify(sigwinch, syscall.SIGWINCH)

	promptMaxTokens := shellPromptWindowForModel(
		this.Config.ShellPromptModel,
		this.Config.ShellMaxPromptTokens,
	)
	autoSuggestMaxTokens := min(
		NumTokensForModel(this.Config.ShellAutosuggestModel),
		this.Config.ShellMaxPromptTokens)

	shellState := &ShellState{
		Butterfish:                   this,
		ParentOut:                    parentOut,
		ChildIn:                      childIn,
		Sigwinch:                     sigwinch,
		State:                        stateNormal,
		ChildOutReader:               childOutReader,
		ParentInReader:               parentInReader,
		CursorPosChan:                parentPositionChan,
		PrintErrorChan:               make(chan error, 8),
		History:                      NewShellHistory(),
		PromptOutputChan:             make(chan *util.CompletionResponse),
		PromptAnswerWriter:           styleCodeblocksWriter,
		PromptGoalAnswerWriter:       styleCodeblocksWriterGoal,
		StyleWriter:                  styleCodeblocksWriter,
		Command:                      NewShellBuffer(),
		Prompt:                       NewShellBuffer(),
		TerminalWidth:                termWidth,
		AutosuggestEnabled:           this.Config.ShellAutosuggestEnabled,
		AutosuggestChan:              make(chan *AutosuggestResult),
		Color:                        colorScheme,
		parentInBuffer:               []byte{},
		PromptMaxTokens:              promptMaxTokens,
		AutosuggestMaxTokens:         autoSuggestMaxTokens,
		RunningChildrenCheckInterval: 250 * time.Millisecond,
		HasRunningChildrenFn:         HasRunningChildren,
		TUITailMaxBytes:              4096,
	}

	shellState.Prompt.SetTerminalWidth(termWidth)
	shellState.Prompt.SetColor(colorScheme.Prompt)

	go readerToChannel(childOut, childOutReader)
	go readerToChannelWithPosition(parentIn, parentInReader, parentPositionChan)

	// clear out any existing output to hide the PS1 export stuff
	clearByteChan(childOutReader, 1000*time.Millisecond)

	// start
	shellState.Mux()
}

func (this *ShellState) Errorf(format string, args ...any) {
	this.PrintErrorChan <- fmt.Errorf(format, args...)
}

func (this *ShellState) PrintError(err error) {
	this.PrintErrorChan <- err
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func (this *ShellState) writeChild(data []byte) {
	if len(data) == 0 {
		return
	}
	if err := writeAll(this.ChildIn, data); err != nil {
		log.Printf("Error writing child input: %s", err)
	}
}

// We're asking GPT to generate bash commands, which can use some escapes
// like \' which aren't valid JSON but are valid bash. This function identifies
// those and adds an extra escape so that the JSON is valid.
func AddDoubleEscapesForJSON(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	return s
}

type CommandParams struct {
	Cmd string `json:"cmd"`
}

var commandRegex = regexp.MustCompile("^\\s*\\{\\s*\"cmd\"\\s*:\\s*\"(.*)\"\\s*\\}\\s*$")

// Parse the arguments from the command function returned in a Chat completion.
// We parse this with a regex rather than unmarshalling because the command
// may contain unescaped quotes, which would cause the unmarshal to fail.
func parseCommandParams(params string) (string, error) {
	// get cmd value using commandRegex
	matches := commandRegex.FindStringSubmatch(params)
	if len(matches) != 2 {
		return "", fmt.Errorf("Unable to parse command params: %s", params)
	}
	cmd := matches[1]

	// check for an uneven number of quotes
	if strings.Count(cmd, "\"")%2 == 1 {
		log.Printf("Uneven number of double quotes in command: %s", cmd)
	}
	if strings.Count(cmd, "'")%2 == 1 {
		log.Printf("Uneven number of single quotes in command: %s", cmd)
	}

	return cmd, nil
}

type UserInputParams struct {
	Question string `json:"question"`
}

func parseUserInputParams(params string) (string, error) {
	// unmarshal UserInputParams from FunctionParameters
	var userInputParams UserInputParams
	err := json.Unmarshal([]byte(params), &userInputParams)
	return userInputParams.Question, err
}

type FinishParams struct {
	Success bool `json:"success"`
}

func parseFinishParams(params string) (bool, error) {
	// unmarshal FinishParams from FunctionParameters
	var finishParams FinishParams
	err := json.Unmarshal([]byte(params), &finishParams)
	return finishParams.Success, err
}

// TODO add a diagram of streams here
func (this *ShellState) Mux() {
	log.Printf("Started shell mux")
	childOutBuffer := []byte{}

	for {
		select {
		case <-this.Butterfish.Ctx.Done():
			return

		case err := <-this.PrintErrorChan:
			log.Printf("Error: %s", err.Error())
			this.History.Append(historyTypeShellOutput, err.Error())
			fmt.Fprintf(this.ParentOut, "%s%s", this.Color.Error, err.Error())
			this.setState(stateNormal)
			fmt.Fprintf(this.ChildIn, "\n")

		// The CursorPosChan produces cursor positions seen in the parent input,
		// which have then been cleaned from the incoming text. If we find a
		// position in this case it means that a child process has requested
		// the cursor position (rather than butterfish shell), and so we re-add
		// the position to the child input. The other case is when we call
		// GetCursorPosition(), which blocks this process until we get a valid
		// position.
		case pos := <-this.CursorPosChan:
			fmt.Fprintf(this.ChildIn, "\x1b[%d;%dR", pos.Row, pos.Column)

		// the terminal window resized and we got a SIGWINCH
		case <-this.Sigwinch:
			termWidth, _, err := term.GetSize(int(os.Stdout.Fd()))
			if err != nil {
				log.Printf("Error getting terminal size after SIGWINCH: %s", err)
			}
			if this.Butterfish.Config.Verbose > 0 {
				log.Printf("Got SIGWINCH with new width %d", termWidth)
			}
			this.TerminalWidth = termWidth
			this.Prompt.SetTerminalWidth(termWidth)
			this.StyleWriter.SetTerminalWidth(termWidth)
			if this.AutosuggestBuffer != nil {
				this.AutosuggestBuffer.SetTerminalWidth(termWidth)
			}
			if this.Command != nil {
				this.Command.SetTerminalWidth(termWidth)
			}

		// We received an autosuggest result from the autosuggest goroutine
		case result := <-this.AutosuggestChan:
			// request cursor position
			_, col := this.GetCursorPosition()
			var buffer *ShellBuffer

			// figure out which buffer we're autocompleting
			switch this.State {
			case statePrompting:
				buffer = this.Prompt
			case stateShell, stateNormal:
				buffer = this.Command
			case statePromptResponse:
				continue
			default:
				log.Printf("Got autosuggest result in unexpected state %d", this.State)
				continue
			}

			this.ShowAutosuggest(buffer, result, col-1, this.TerminalWidth)

		// We got an LLM prompt response, handle the response by adding to history,
		// calling functions returned, etc.
		case output := <-this.PromptOutputChan:
			historyData := output.Completion
			if historyData != "" {
				this.History.Append(historyTypeLLMOutput, historyData)
			}
			if output.FunctionName != "" {
				callID := ""
				if len(output.ToolCalls) > 0 {
					callID = output.ToolCalls[0].Id
				}
				this.History.AddFunctionCall(output.FunctionName, output.FunctionParameters, callID)
			}
			if len(output.ShellCalls) > 0 {
				for _, shellCall := range output.ShellCalls {
					this.History.AddShellCall(shellCall)
				}
			}

			// If there is child output waiting to be printed, print that now
			if len(childOutBuffer) > 0 {
				this.ParentOut.Write(childOutBuffer)
				this.History.Append(historyTypeShellOutput, string(childOutBuffer))
				childOutBuffer = []byte{}
			}

			// Get a new prompt
			this.writeChild([]byte("\n"))

			if this.GoalMode {
				this.ActiveFunction = output.FunctionName
				if len(output.ToolCalls) > 0 {
					this.ActiveFunctionCallID = output.ToolCalls[0].Id
				} else if len(output.ShellCalls) > 0 {
					this.ActiveFunction = "shell"
					this.ActiveFunctionCallID = output.ShellCalls[0].CallID
					this.ActiveShellMaxOutputLength = output.ShellCalls[0].MaxOutputLength
				} else {
					this.ActiveFunctionCallID = ""
				}
				this.GoalModeFunction(output)
				if this.GoalMode {
					continue
				}
			}

			this.RequestAutosuggest(0, "")
			this.setState(stateNormal)
			this.ParentInputLoop([]byte{})

		case childOutMsg := <-this.ChildOutReader:
			if childOutMsg == nil {
				log.Println("Child out reader closed")
				this.Butterfish.Cancel()
				return
			}

			if this.Butterfish.Config.Verbose > 2 {
				log.Printf("Child out: %x", string(childOutMsg.Data))
			}

			// When an interactive child (e.g. neovim) is active, bypass costly prompt
			// parsing/history updates and forward bytes directly.
			if this.State == stateNormal && !this.GoalMode && this.ActiveFunction == "" {
				hasPromptMarkers := bytes.Contains(childOutMsg.Data, []byte(PROMPT_PREFIX)) ||
					bytes.Contains(childOutMsg.Data, []byte(PROMPT_SUFFIX))
				runningChildren := this.hasRunningChildrenCached(false)

				if this.InteractiveChildPassthrough {
					if runningChildren && !hasPromptMarkers {
						this.appendTUITail(childOutMsg.Data)
						this.ParentOut.Write(childOutMsg.Data)
						continue
					}
					this.InteractiveChildPassthrough = false
					this.flushTUITailToHistory()
				} else if runningChildren && !hasPromptMarkers && likelyTUIControlSequence(childOutMsg.Data) {
					this.InteractiveChildPassthrough = true
					this.appendTUITail(childOutMsg.Data)
					this.ParentOut.Write(childOutMsg.Data)
					continue
				}
			}

			lastStatus, prompts, childOutStr := this.ParsePS1(string(childOutMsg.Data))
			this.PromptSuffixCounter += prompts

			if prompts > 0 && this.State == stateNormal && !this.GoalMode {
				// Prompt observed means child command is done.
				this.RunningChildren = false
				this.RunningChildrenLastCheck = time.Now()
				this.InteractiveChildPassthrough = false

				// If we get a prompt and we're at the start of a command
				// then we should request autosuggest
				newAutosuggestDelay := this.Butterfish.Config.ShellNewlineAutosuggestTimeout
				if newAutosuggestDelay >= 0 {
					this.RequestAutosuggest(newAutosuggestDelay, "")
				}
			}

			// If we're actively printing a response we buffer child output
			if this.State == statePromptResponse {
				// In goal mode we throw it away
				if !this.GoalMode {
					childOutBuffer = append(childOutBuffer, childOutStr...)
				}
				continue
			}

			endOfFunctionCall := false
			if this.GoalMode {
				this.GoalModeBuffer += childOutStr
				if this.PromptSuffixCounter >= 2 {
					// this means that since starting to collect command function call
					// output, we've seen two prompts, which means the function call
					// is done and we can send the response back to the model
					endOfFunctionCall = true
				}
			} else if this.ActiveFunction != "" {
				this.ActiveFunction = ""
				this.ActiveFunctionCallID = ""
			}

			// If we're getting child output while typing in a shell command, this
			// could mean the user is paging through old commands, or doing a tab
			// completion, or something unknown, so we don't want to add to history.
			if this.State != stateShell && !this.FilterChildOut(string(childOutMsg.Data)) {
				if this.ActiveFunction != "" {
					this.History.AppendFunctionOutput(this.ActiveFunction, this.ActiveFunctionCallID, childOutStr)
				} else {
					this.History.Append(historyTypeShellOutput, childOutStr)
				}
			}

			// If the user is in shell mode and presses tab, and we're not doing a
			// butterfish autocomplete, then we want to edit the command buffer with
			// whatever the shell outputs immediately after tab. We treat stuff
			// printed in a 50ms window as part of the tab completion.
			var AUTOSUGGEST_TAB_WINDOW = 50 * time.Millisecond
			timestamp := time.Now()

			if this.State == stateShell {
				timeSinceTab := timestamp.Sub(this.LastTabPassthrough)
				if timeSinceTab < AUTOSUGGEST_TAB_WINDOW {
					if this.Butterfish.Config.Verbose > 1 {
						log.Printf("Time since tab: %s, adding to command: %s",
							timeSinceTab, childOutStr)
					}
					this.Command.Write(childOutStr)
					this.RefreshAutosuggest([]byte(childOutStr), this.Command, this.Color.Command)
				}
			}

			this.ParentOut.Write([]byte(childOutStr))

			if endOfFunctionCall {
				// move cursor to the beginning of the line and clear the line
				fmt.Fprintf(this.ParentOut, "\r%s", ESC_CLEAR)
				var status string
				if this.ActiveFunction == "command" {
					status = fmt.Sprintf("Exit Code: %d\n", lastStatus)
					this.GoalModeFunctionResponse(status)
				} else if this.ActiveFunction == "shell" {
					this.GoalModeShellCallResponse(lastStatus)
				} else {
					this.GoalModeFunctionResponse(status)
				}
				this.ActiveFunction = ""
				this.ActiveFunctionCallID = ""
				this.ActiveShellMaxOutputLength = 0
				this.GoalModeBuffer = ""
				this.PromptSuffixCounter = 0
			}

		case parentInMsg := <-this.ParentInReader:
			if parentInMsg == nil {
				log.Println("Parent in reader closed")
				this.Butterfish.Cancel()
				return
			}

			this.ParentInputLoop(parentInMsg.Data)
		}
	}
}

func (this *ShellState) ParentInputLoop(data []byte) {
	if this.Butterfish.Config.Verbose > 2 {
		log.Printf("Parent in: %x", data)
	}

	// include any cached data
	if len(this.parentInBuffer) > 0 {
		data = append(this.parentInBuffer, data...)
		this.parentInBuffer = []byte{}
	}

	if len(data) == 0 {
		return
	}

	// If we've started an ANSI escape sequence, it might not be complete
	// yet, so we need to cache it and wait for the next message
	if incompleteAnsiSequence(data) {
		this.parentInBuffer = append(this.parentInBuffer, data...)
		return
	}

	for {
		// The InputFromParent function consumes bytes from the passed in data
		// buffer and returns unprocessed bytes, so we loop and continue to
		// pass data in, if available
		leftover := this.ParentInput(this.Butterfish.Ctx, data)

		if leftover == nil || len(leftover) == 0 {
			break
		}
		if len(leftover) == len(data) {
			// nothing was consumed, we buffer and try again later
			this.parentInBuffer = append(this.parentInBuffer, leftover...)
			break
		}

		// go again with the leftover data
		data = leftover
	}
}

func (this *ShellState) ParentInput(ctx context.Context, data []byte) []byte {
	hasCarriageReturn := bytes.Contains(data, []byte{'\r'})

	switch this.State {
	case statePromptResponse:
		// Ctrl-C while receiving prompt
		// We're buffering the input right now so we check both the first and last
		// bytes for Ctrl-C
		if data[0] == 0x03 || data[len(data)-1] == 0x03 {
			log.Printf("Canceling prompt response")
			this.PromptResponseCancel()
			this.PromptResponseCancel = nil
			this.GoalMode = false
			this.setState(stateNormal)
			if data[0] == 0x03 {
				return data[1:]
			} else {
				return data[:len(data)-1]
			}
		}

		// If we're in the middle of a prompt response we ignore all other input
		return data

	case stateNormal:
		if this.hasRunningChildrenCached(false) {
			// If we have running children then the shell is running something,
			// so just forward the input.
			this.writeChild(data)
			return nil
		}

		first := data[:1]

		if data[0] == 0x03 {
			if this.GoalMode {
				// Ctrl-C while in goal mode
				fmt.Fprintf(this.PromptGoalAnswerWriter, "\n%sExited goal mode.%s\n", this.Color.Answer, this.Color.Command)
				this.GoalMode = false
			}

			if this.Command != nil {
				this.Command.Clear()
			}
			if this.Prompt != nil {
				this.Prompt.Clear()
			}
			this.setState(stateNormal)
			this.writeChild([]byte{data[0]})

			return data[1:]
		}

		// Check if the first character is uppercase or a bang
		if unicode.IsUpper(rune(data[0])) || data[0] == '!' {
			this.setState(statePrompting)
			this.ClearAutosuggest(this.Color.Command)
			this.Prompt.Clear()
			this.Prompt.Write(string(first))

			// Write the actual prompt start
			color := this.Color.Prompt
			if data[0] == '!' {
				color = this.Color.PromptGoal
			}
			this.Prompt.SetColor(color)
			fmt.Fprintf(this.ParentOut, "%s%s", color, first)

			// We're starting a prompt managed here in the wrapper, so we want to
			// get the cursor position
			_, col := this.GetCursorPosition()
			this.Prompt.SetPromptLength(col - 1 - this.Prompt.Size())
			return data[1:]

		} else if data[0] == '\t' { // user is asking to fill in an autosuggest
			if this.LastAutosuggest != "" {
				this.RealizeAutosuggest(this.Command, true, this.Color.Command)
				this.setState(stateShell)
				return data[1:]
			} else {
				// no last autosuggest found, just forward the tab
				this.LastTabPassthrough = time.Now()
				this.writeChild([]byte{data[0]})
			}
			return data[1:]

		} else if data[0] == '\r' {
			this.ClearAutosuggest(this.Color.Command)
			this.writeChild(first)
			return data[1:]

		} else {
			// Only consume the first printable byte when transitioning from
			// stateNormal to stateShell/statePrompting. If we consume an entire paste
			// burst here we can duplicate input and skip stateShell carriage handling.
			consumed := first
			leftover := data[1:]
			if data[0] == 0x1b {
				// Preserve full ANSI control sequences while idle in stateNormal.
				consumed = data
				leftover = nil
			}

			this.Command = NewShellBuffer()
			this.Command.Write(string(consumed))

			if this.Command.Size() > 0 {
				// this means that the command is not empty, i.e. the input wasn't
				// some control character
				this.RefreshAutosuggest(consumed, this.Command, this.Color.Command)
				this.setState(stateShell)
			} else {
				this.ClearAutosuggest(this.Color.Command)
			}

			this.ParentOut.Write([]byte(this.Color.Command))
			this.writeChild(consumed)
			return leftover
		}

	case statePrompting:
		if hasCarriageReturn {
			// check if the input contains a newline
			this.ClearAutosuggest(this.Color.Command)
			index := bytes.Index(data, []byte{'\r'})
			toAdd := data[:index]
			toPrint := this.Prompt.Write(string(toAdd))

			this.ParentOut.Write(toPrint)
			this.ParentOut.Write([]byte("\n\r"))

			promptStr := this.Prompt.String()
			if this.HandleLocalPrompt() {
				// This was a local prompt like "help", we're done now
				return data[index+1:]
			}

			if promptStr[0] == '!' {
				this.GoalModeStart()
			} else if this.GoalMode {
				this.GoalModeChat()
			} else {
				this.SendPrompt()
			}
			return data[index+1:]

		} else if data[0] == '!' && this.Prompt.String() == "!" {
			// If the user is prefixing the prompt with two bangs then they may
			// be entering unsafe goal mode, color the prompt accordingly
			this.Prompt.SetColor(this.Color.PromptGoalUnsafe)
			toPrint := this.Prompt.Write(string(data))
			this.ParentOut.Write(toPrint)

		} else if data[0] == '\t' { // user is asking to fill in an autosuggest
			// Tab was pressed, fill in lastAutosuggest
			if this.LastAutosuggest != "" {
				this.RealizeAutosuggest(this.Prompt, false, this.Color.Prompt)
			} else {
				// no last autosuggest found, just forward the tab
				this.ParentOut.Write(data)
			}

			return data[1:]

		} else if data[0] == 0x03 { // Ctrl-C
			if this.PromptResponseCancel != nil {
				this.PromptResponseCancel()
				this.PromptResponseCancel = nil
			}
			this.ClearAutosuggest(this.Color.Command)
			toPrint := this.Prompt.Clear()
			this.ParentOut.Write(toPrint)
			this.ParentOut.Write([]byte(this.Color.Command))
			this.setState(stateNormal)
			return data[1:]

		} else { // otherwise user is typing a prompt
			toPrint := this.Prompt.Write(string(data))
			this.RefreshAutosuggest(data, this.Prompt, this.Color.Prompt)
			this.ParentOut.Write(toPrint)

			if this.Prompt.Size() == 0 {
				this.ParentOut.Write([]byte(this.Color.Command)) // reset color
				this.setState(stateNormal)
			}
		}

	case stateShell:
		if hasCarriageReturn { // user is submitting a command
			this.ClearAutosuggest(this.Color.Command)

			this.setState(stateNormal)

			index := bytes.Index(data, []byte{'\r'})
			// Keep command history in sync for paste bursts that include '\r'
			// in the same read chunk.
			if index > 0 {
				this.Command.Write(string(data[:index]))
			}
			this.writeChild(data[:index+1])
			this.History.Append(historyTypeShellInput, this.Command.String())
			this.Command = NewShellBuffer()

			if this.AutosuggestCancel != nil {
				// We'll likely have a pending autosuggest in the background, cancel it
				this.AutosuggestCancel()
			}

			return data[index+1:]

		} else if data[0] == 0x03 { // Ctrl-C
			this.Command.Clear()
			this.setState(stateNormal)
			this.writeChild([]byte{data[0]})

			if this.AutosuggestCancel != nil {
				// We'll likely have a pending autosuggest in the background, cancel it
				this.AutosuggestCancel()
			}

			return data[1:]

		} else if data[0] == '\t' { // user is asking to fill in an autosuggest
			// Tab was pressed, fill in lastAutosuggest
			if this.LastAutosuggest != "" {
				this.RealizeAutosuggest(this.Command, true, this.Color.Command)
			} else {
				// no last autosuggest found, just forward the tab
				this.LastTabPassthrough = time.Now()
				this.writeChild([]byte{data[0]})
			}
			return data[1:]

		} else { // otherwise user is typing a command
			this.Command.Write(string(data))
			this.RefreshAutosuggest(data, this.Command, this.Color.Command)
			this.writeChild(data)
			if this.Command.Size() == 0 {
				this.setState(stateNormal)
			}
		}

	default:
		panic("Unknown state")
	}

	return nil
}

// We want to queue up the prompt response, which does the processing (except
// for actually printing it). The processing like adding to history or
// executing the next step in goal mode. We have to do this in a goroutine
// because otherwise we would block the main thread.
func (this *ShellState) SendPromptResponse(data string) {
	go func() {
		this.PromptOutputChan <- &util.CompletionResponse{Completion: data}
	}()
}

func (this *ShellState) PrintStatus() {
	text := fmt.Sprintf("You're using Butterfish Shell\n%s\n\n", this.Butterfish.Config.BuildInfo)

	if this.GoalMode {
		text += fmt.Sprintf("You're in Goal mode, the goal you've given to the agent is:\n%s\n\n", this.GoalModeGoal)
	}

	text += fmt.Sprintf("Prompting model:       %s\n", this.Butterfish.Config.ShellPromptModel)
	text += fmt.Sprintf("Prompt history window: %d tokens\n", this.PromptMaxTokens)
	text += fmt.Sprintf("Autosuggest:           %t\n", this.Butterfish.Config.ShellAutosuggestEnabled)
	text += fmt.Sprintf("Autosuggest model:     %s\n", this.Butterfish.Config.ShellAutosuggestModel)
	text += fmt.Sprintf("Autosuggest timeout:   %s\n", this.Butterfish.Config.ShellAutosuggestTimeout)
	text += fmt.Sprintf("Autosuggest history:   %d tokens\n", this.AutosuggestMaxTokens)
	fmt.Fprintf(this.PromptAnswerWriter, "%s%s%s", this.Color.Answer, text, this.Color.Command)
	this.SendPromptResponse(text)
}

func (this *ShellState) PrintHelp() {
	text := `You're using the Butterfish Shell Mode, which means you have a Butterfish wrapper around your normal shell. Here's how you use it:

	- Type a normal command, like "ls -l" and press enter to execute it
	- Start a command with a capital letter to send it to GPT, like "How do I find local .py files?"
	- Autosuggest will print command completions, press tab to fill them in
	- GPT will be able to see your shell history, so you can ask contextual questions like "why didn't my last command work?"
	- Type "Status" to show the current Butterfish configuration
	- Type "History" to show the recent history that will be sent to GPT
`
	fmt.Fprintf(this.PromptAnswerWriter, "%s%s%s", this.Color.Answer, text, this.Color.Command)
	this.SendPromptResponse(text)
}

func (this *ShellState) PrintHistory() {
	maxHistoryBlockTokens := this.Butterfish.Config.ShellMaxHistoryBlockTokens
	historyBlocks, _ := getHistoryBlocksByTokens(this.History, this.getPromptEncoder(),
		maxHistoryBlockTokens, this.PromptMaxTokens, 4)
	strBuilder := strings.Builder{}

	for _, block := range historyBlocks {
		// block header
		strBuilder.WriteString(fmt.Sprintf("%s%s\n", this.Color.GoalMode, HistoryTypeToString(block.Type)))
		blockColor := this.Color.Command
		switch block.Type {
		case historyTypePrompt:
			blockColor = this.Color.Prompt
		case historyTypeLLMOutput:
			blockColor = this.Color.Answer
		case historyTypeShellInput:
			blockColor = this.Color.PromptGoal
		}

		strBuilder.WriteString(fmt.Sprintf("%s%s\n", blockColor, block.Content))
	}

	this.History.LogRecentHistory()
	fmt.Fprintf(this.PromptAnswerWriter, "%s%s", strBuilder.String(), this.Color.Command)
	this.SendPromptResponse("")
}

func (this *ShellState) GoalModeStart() {
	// Get the prompt after the bang
	goal := this.Prompt.String()[1:]
	if goal == "" {
		return
	}

	// If the prompt is preceded with two bangs then go to unsafe mode
	if goal[0] == '!' {
		goal = goal[1:]
		this.GoalModeUnsafe = true
	} else {
		this.GoalModeUnsafe = false
	}

	this.GoalMode = true
	fmt.Fprintf(this.PromptGoalAnswerWriter, "%sGoal mode starting...%s\n", this.Color.Answer, this.Color.Command)
	this.GoalModeGoal = goal
	this.Prompt.Clear()

	prompt := "Start now."
	log.Printf("Starting goal mode: %s", this.GoalModeGoal)
	this.goalModePrompt(prompt)
}

func (this *ShellState) GoalModeChat() {
	prompt := this.Prompt.String()
	this.Prompt.Clear()

	log.Printf("Goal mode chat: %s\n", prompt)
	this.goalModePrompt(prompt)
}

func (this *ShellState) GoalModeFunctionResponse(output string) {
	log.Printf("Goal mode response: %s\n", output)
	if output != "" {
		this.History.AppendFunctionOutput(this.ActiveFunction, this.ActiveFunctionCallID, output)
	}
	this.ActiveFunction = ""
	this.ActiveFunctionCallID = ""
	this.goalModePrompt("")
}

func (this *ShellState) GoalModeShellCallResponse(exitStatus int) {
	output := strings.TrimSpace(this.GoalModeBuffer)
	stderr := ""
	// Goal mode captures PTY output as a single stream; if the command failed,
	// include it as stderr so the model can reason about errors.
	if exitStatus != 0 {
		stderr = output
	}
	shellOutput := &util.ShellCallOutput{
		CallID:          this.ActiveFunctionCallID,
		MaxOutputLength: this.ActiveShellMaxOutputLength,
		Output: []util.ShellCallOutputItem{
			{
				Stdout: output,
				Stderr: stderr,
				Outcome: util.ShellCallOutcome{
					Type:     "exit",
					ExitCode: exitStatus,
				},
			},
		},
	}

	this.History.AppendShellCallOutput(shellOutput)
	this.ActiveFunction = ""
	this.ActiveFunctionCallID = ""
	this.ActiveShellMaxOutputLength = 0
	this.goalModePrompt("")
}

func skippedShellCallOutput(call *util.ShellCall) *util.ShellCallOutput {
	if call == nil || call.CallID == "" {
		return nil
	}

	return &util.ShellCallOutput{
		CallID:          call.CallID,
		MaxOutputLength: call.MaxOutputLength,
		Output: []util.ShellCallOutputItem{
			{
				Stdout: "",
				Stderr: "skipped: butterfish only executes the first shell_call in a response",
				Outcome: util.ShellCallOutcome{
					Type:     "exit",
					ExitCode: 1,
				},
			},
		},
	}
}

func (this *ShellState) GoalModeFunction(output *util.CompletionResponse) {
	if output.Error != "" {
		fmt.Fprintf(this.PromptGoalAnswerWriter, "%sGoal mode error: %s%s\n", this.Color.Error, output.Error, this.Color.Command)
		this.GoalMode = false
		return
	}
	if len(output.ShellCalls) > 0 {
		// The Responses API can return multiple shell_call items. We execute the
		// first one and acknowledge the rest so follow-up requests don't fail due
		// to missing tool outputs.
		for i := 1; i < len(output.ShellCalls); i++ {
			if skipped := skippedShellCallOutput(output.ShellCalls[i]); skipped != nil {
				this.History.AppendShellCallOutput(skipped)
			}
		}

		call := output.ShellCalls[0]
		this.GoalModeBuffer = ""
		this.PromptSuffixCounter = 0
		this.setState(stateNormal)
		if len(call.Commands) == 0 {
			this.GoalModeShellCallResponse(0)
			return
		}

		log.Printf("Goal mode shell_call: %v", call.Commands)
		command := strings.Join(call.Commands, "\n")
		fmt.Fprintf(this.ChildIn, "%s", command)
		if this.GoalModeUnsafe {
			fmt.Fprintf(this.ChildIn, "\n")
		}
		return
	}

	switch output.FunctionName {
	case "command":
		log.Printf("Goal mode command: %s", output.FunctionParameters)
		this.GoalModeBuffer = ""
		this.PromptSuffixCounter = 0
		this.setState(stateNormal)
		cmd, err := parseCommandParams(output.FunctionParameters)
		if err != nil {
			// we failed to parse the command json, send error back to model
			log.Printf("Error parsing function arguments: %s", err)
			modelStr := fmt.Sprintf("Error parsing your json, try again: %s", err)
			this.GoalModeFunctionResponse(modelStr)
			return
		}
		log.Printf("Goal mode command: %s", cmd)
		fmt.Fprintf(this.ChildIn, "%s", cmd)
		if this.GoalModeUnsafe {
			fmt.Fprintf(this.ChildIn, "\n")
		}

	case "user_input":
		log.Printf("Goal mode user_input: %s", output.FunctionParameters)
		this.GoalModeBuffer = ""
		this.PromptSuffixCounter = -999999
		this.setState(stateNormal)
		question, err := parseUserInputParams(output.FunctionParameters)
		if err != nil {
			log.Printf("Error parsing function arguments: %s", err)
			modelStr := fmt.Sprintf("Error parsing your json, try again: %s", err)
			this.GoalModeFunctionResponse(modelStr)
			return
		}

		fmt.Fprintf(this.PromptAnswerWriter, "%s%s%s\n", this.Color.Answer, question, this.Color.Command)

	case "finish":
		log.Printf("Goal mode finishing: %s", output.FunctionParameters)
		this.GoalModeBuffer = ""
		this.setState(stateNormal)
		success, err := parseFinishParams(output.FunctionParameters)
		if err != nil {
			log.Printf("Error parsing function arguments: %s", err)
			modelStr := fmt.Sprintf("Error parsing your json, try again: %s", err)
			this.History.AppendFunctionOutput(output.FunctionName, this.ActiveFunctionCallID, modelStr)
			this.GoalModeFunctionResponse(modelStr)
			return
		}
		this.History.AppendFunctionOutputAllowEmpty(output.FunctionName, this.ActiveFunctionCallID)

		result := "SUCCESS"
		if !success {
			result = "FAILURE"
		}

		fmt.Fprintf(this.PromptGoalAnswerWriter, "%sExited goal mode with %s.%s\n", this.Color.Answer, result, this.Color.Command)
		this.GoalMode = false

	case "":
		log.Printf("No function called in goal mode")
		modelStr := fmt.Sprintf("You must call a function in goal mode responses.")
		this.History.Append(historyTypePrompt, modelStr)
		this.GoalModeFunctionResponse("")

	default:
		log.Printf("Invalid function name called in goal mode: %s", output.FunctionName)
		modelStr := fmt.Sprintf("Invalid function name: %s", output.FunctionName)
		this.GoalModeFunctionResponse(modelStr)

	}
}

var goalModeFunctions = []util.FunctionDefinition{
	{
		Name:        "command",
		Description: "Run a command in the shell to help achieve your goal",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"cmd": map[string]any{
					"type":        "string",
					"description": "The string command including any arguments, for example 'ls ~'",
				},
			},
			"required": []string{"cmd"},
		},
	},

	{
		Name:        "user_input",
		Description: "Resolve an ambiguity in the goal or provide additional information or hand off a goal that can't be accomplished to the user.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"question": map[string]any{
					"type":        "string",
					"description": "The question to ask the user",
				},
			},
			"required": []string{"question"},
		},
	},

	{
		Name:        "finish",
		Description: "Finish the goal and exit goal mode, call only if the goal is accomplished or multiple strategies have been attempted and the goal is impossible.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"success": map[string]any{
					"type":        "boolean",
					"description": "Whether the goal was accomplished",
				},
			},
			"required": []string{"success"},
		},
	},
}

var goalModeFunctionsShellTool = []util.FunctionDefinition{
	goalModeFunctions[1],
	goalModeFunctions[2],
}

var goalModeFunctionsStringCache = map[string]string{}

// serialize goal mode functions to json and cache by name list
func getGoalModeFunctionsString(functions []util.FunctionDefinition) string {
	names := make([]string, 0, len(functions))
	for _, fn := range functions {
		names = append(names, fn.Name)
	}
	key := strings.Join(names, ",")
	if cached, ok := goalModeFunctionsStringCache[key]; ok {
		return cached
	}
	bytes, err := json.Marshal(functions)
	if err != nil {
		log.Fatal(err)
	}
	goalModeFunctionsStringCache[key] = string(bytes)
	return goalModeFunctionsStringCache[key]
}

func supportsShellToolModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "gpt-5.1") || strings.HasPrefix(model, "gpt-5.2")
}

func (this *ShellState) goalModePrompt(lastPrompt string) {
	this.setState(statePromptResponse)
	requestCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	this.PromptResponseCancel = cancel

	sysMsg, err := this.Butterfish.PromptLibrary.GetPrompt(
		prompt.GoalModeSystemMessage,
		"goal", this.GoalModeGoal,
		"sysinfo", GetSystemInfo())
	if err != nil {
		msg := fmt.Errorf("ERROR: could not retrieve prompting system message: %s", err)
		log.Println(msg)
		this.PrintError(msg)
		return
	}

	useShellTool := supportsShellToolModel(this.Butterfish.Config.ShellPromptModel)
	functions := goalModeFunctions
	tools := []util.ToolDefinition{}
	if useShellTool {
		functions = goalModeFunctionsShellTool
		tools = append(tools, util.ToolDefinition{Type: "shell"})
		sysMsg += "\n\nUse the shell tool to run commands. Use user_input to ask clarifying questions and finish when done."
	}

	tokensForAnswer := 1024
	lastPrompt, historyBlocks, err := this.AssembleChat(lastPrompt, sysMsg, getGoalModeFunctionsString(functions), tokensForAnswer)
	if err != nil {
		this.PrintError(err)
		return
	}

	request := &util.CompletionRequest{
		Ctx:           requestCtx,
		Prompt:        lastPrompt,
		Model:         this.Butterfish.Config.ShellPromptModel,
		MaxTokens:     tokensForAnswer,
		Temperature:   0.6,
		HistoryBlocks: historyBlocks,
		SystemMessage: sysMsg,
		Functions:     functions,
		Tools:         tools,
		Verbose:       this.Butterfish.Config.Verbose > 0,
	}

	// we run this in a goroutine so that we can still receive input
	// like Ctrl-C while waiting for the response
	go CompletionRoutine(request, this.Butterfish.LLMClient,
		this.PromptGoalAnswerWriter, this.PromptOutputChan,
		this.Color.GoalMode, this.Color.Error, this.StyleWriter)
}

func (this *ShellState) HandleLocalPrompt() bool {
	promptStr := strings.ToLower(this.Prompt.String())
	promptStr = strings.TrimSpace(promptStr)

	switch promptStr {
	case "status":
		this.PrintStatus()
	case "help":
		this.PrintHelp()
	case "history":
		this.PrintHistory()
	default:
		return false
	}

	return true
}

// Given an encoder, a string, and a maximum number of takens, we count the
// number of tokens in the string and truncate to the max tokens if the would
// exceed it. Returns the number of tokens, the truncated string, and a bool
// indicating whether the string was truncated.
func countAndTruncate(data string,
	encoder *tiktoken.Tiktoken,
	maxTokens int) (int, string, bool) {
	tokens := encoder.Encode(data, nil, nil)
	truncated := false
	if len(tokens) >= maxTokens {
		tokens = tokens[:maxTokens]
		data = encoder.Decode(tokens)
		truncated = true
	}

	return len(tokens), data, truncated
}

// Prepare to call assembleChat() based on the ShellState variables for
// calculating token limits.
func (this *ShellState) AssembleChat(prompt, sysMsg, functions string, reserveForAnswer int) (string, []util.HistoryBlock, error) {
	// How many tokens can this model handle
	totalTokens := this.PromptMaxTokens
	maxPromptTokens := 512 // for the prompt specifically
	// for each individual history block
	maxHistoryBlockTokens := this.Butterfish.Config.ShellMaxHistoryBlockTokens
	// How much for the total request (prompt, history, sys msg)
	maxCombinedPromptTokens := totalTokens - reserveForAnswer

	return assembleChat(prompt, sysMsg, functions, this.History,
		this.Butterfish.Config.ShellPromptModel, this.getPromptEncoder(),
		maxPromptTokens, maxHistoryBlockTokens, maxCombinedPromptTokens)
}

// Build a list of HistoryBlocks for use in GPT chat history, and ensure the
// prompt and system message plus the history are within the token limit.
// The prompt may be truncated based on maxPromptTokens.
func assembleChat(
	prompt string,
	sysMsg string,
	functions string,
	history *ShellHistory,
	model string,
	encoder *tiktoken.Tiktoken,
	maxPromptTokens int,
	maxHistoryBlockTokens int,
	maxTokens int,
) (string, []util.HistoryBlock, error) {

	tokensPerMessage := NumTokensPerMessageForModel(model)

	// baseline for chat
	usedTokens := 3

	// account for prompt
	numPromptTokens, prompt, truncated := countAndTruncate(prompt, encoder, maxPromptTokens)
	if truncated {
		log.Printf("WARNING: truncated the prompt to %d tokens", numPromptTokens)
	}
	usedTokens += numPromptTokens

	// account for system message
	sysMsgTokens := encoder.Encode(sysMsg, nil, nil)
	if len(sysMsgTokens) > 1028 {
		log.Printf("WARNING: the system message is very long, this may cause you to hit the token limit. Recommend you reduce the size in prompts.yaml")
	}

	usedTokens += usedTokens + len(sysMsgTokens)
	if usedTokens > maxTokens {
		return "", nil, fmt.Errorf("System message too long, %d tokens, max is %d", usedTokens, maxTokens)
	}

	// account for functions
	functionTokens := encoder.Encode(functions, nil, nil)
	if len(functionTokens) > 1028 {
		log.Printf("WARNING: the functions are very long and are taking up %d tokens. This may cause you to hit the token limit.", functionTokens)
	}

	usedTokens += usedTokens + len(functionTokens)
	if usedTokens > maxTokens {
		return "", nil, fmt.Errorf("System message plus functions too long, %d tokens, max is %d", usedTokens, maxTokens)
	}

	blocks, historyTokens := getHistoryBlocksByTokens(
		history,
		encoder,
		maxHistoryBlockTokens,
		maxTokens-usedTokens,
		tokensPerMessage)
	usedTokens += historyTokens

	if usedTokens > maxTokens {
		panic("Too many tokens, this should not happen")
	}

	return prompt, blocks, nil
}

// Iterate through a history and build a list of HistoryBlocks up until the
// maximum number of tokens is reached. A single block will be truncated to
// the maxHistoryBlockTokens number. Each block will start at a baseline of
// tokensPerMessage number of tokens.
// We return the history blocks and the number of tokens it uses.
func getHistoryBlocksByTokens(
	history *ShellHistory,
	encoder *tiktoken.Tiktoken,
	maxHistoryBlockTokens,
	maxTokens,
	tokensPerMessage int,
) ([]util.HistoryBlock, int) {

	blocks := []util.HistoryBlock{}
	usedTokens := 0

	history.IterateBlocks(func(block *HistoryBuffer) bool {
		if block.Content.Size() == 0 && block.FunctionName == "" && block.ToolType == "" && block.ShellCall == nil && block.ShellCallOutput == nil {
			// empty block, skip
			return true
		}
		msgTokens := tokensPerMessage
		roleString := ShellHistoryTypeToRole(block.Type)

		// add tokens for role
		msgTokens += len(encoder.Encode(roleString, nil, nil))

		if block.FunctionName != "" {
			// add tokens for function name
			msgTokens += len(encoder.Encode(block.FunctionName, nil, nil))
		}
		if block.FunctionParams != "" {
			// add tokens for function params
			msgTokens += len(encoder.Encode(block.FunctionParams, nil, nil))
		}

		// check existing block tokenizations
		contentLen := block.Content.Size()
		content, contentTokens, ok := block.GetTokenization(encoder.EncoderName(), contentLen)

		if !ok { // cache miss
			contentStr := block.Content.String()
			// avoid processing super long strings with a ceiling
			ceiling := maxHistoryBlockTokens * 4
			if contentLen > ceiling {
				contentStr = contentStr[:ceiling]
			}

			// remove ANSI escape codes
			historyContent := sanitizeTTYString(contentStr)
			// encode and truncate
			contentTokens, content, _ = countAndTruncate(historyContent, encoder, maxHistoryBlockTokens)
			// save truncated string
			block.SetTokenization(encoder.EncoderName(), contentLen, contentTokens, content)
		}
		msgTokens += contentTokens

		if usedTokens+msgTokens > maxTokens {
			// we're done adding blocks
			return false
		}

		usedTokens += msgTokens
		newBlock := util.HistoryBlock{
			Type:            block.Type,
			Content:         content,
			FunctionName:    block.FunctionName,
			FunctionParams:  block.FunctionParams,
			ToolCallId:      block.ToolCallID,
			ToolType:        block.ToolType,
			ShellCall:       block.ShellCall,
			ShellCallOutput: block.ShellCallOutput,
		}

		// we prepend the block so that the history is in the correct order
		blocks = append([]util.HistoryBlock{newBlock}, blocks...)
		return true
	})

	return blocks, usedTokens
}

func (this *ShellState) SendPrompt() {
	this.setState(statePromptResponse)

	requestCtx, cancel := context.WithCancel(context.Background())
	this.PromptResponseCancel = cancel

	sysMsg, err := this.Butterfish.PromptLibrary.GetPrompt(
		prompt.ShellSystemMessage, "sysinfo", GetSystemInfo())
	if err != nil {
		msg := fmt.Errorf("Could not retrieve prompting system message: %s", err)
		this.PrintError(msg)
		return
	}

	prompt := this.Prompt.String()
	tokensReservedForAnswer := this.Butterfish.Config.ShellMaxResponseTokens
	prompt, historyBlocks, err := this.AssembleChat(prompt, sysMsg, "", tokensReservedForAnswer)
	if err != nil {
		this.PrintError(err)
		return
	}

	request := &util.CompletionRequest{
		Ctx:           requestCtx,
		Prompt:        prompt,
		Model:         this.Butterfish.Config.ShellPromptModel,
		MaxTokens:     tokensReservedForAnswer,
		Temperature:   0.7,
		HistoryBlocks: historyBlocks,
		SystemMessage: sysMsg,
		Verbose:       this.Butterfish.Config.Verbose > 0,
		TokenTimeout:  this.Butterfish.Config.TokenTimeout,
	}

	this.History.Append(historyTypePrompt, this.Prompt.String())

	// we run this in a goroutine so that we can still receive input
	// like Ctrl-C while waiting for the response
	go CompletionRoutine(request, this.Butterfish.LLMClient,
		this.PromptAnswerWriter, this.PromptOutputChan,
		this.Color.Answer, this.Color.Error, this.StyleWriter)

	this.Prompt.Clear()
}

func CompletionRoutine(
	request *util.CompletionRequest,
	client LLM,
	writer io.Writer,
	outputChan chan *util.CompletionResponse,
	normalColor,
	errorColor string,
	styleWriter *util.StyleCodeblocksWriter,
) {
	writer.Write([]byte(normalColor))
	output, err := client.CompletionStream(request, writer)

	// handle any completion errors
	if err != nil {
		errStr := fmt.Sprintf("Error prompting LLM: %s\n", err)

		log.Printf("%s", errStr)

		if !strings.Contains(errStr, "context canceled") {
			fmt.Fprintf(writer, "%s%s", errorColor, errStr)
		}
	}

	if output == nil && err != nil {
		output = &util.CompletionResponse{Completion: err.Error(), Error: err.Error()}
	} else if output != nil && err != nil {
		output.Error = err.Error()
	}

	if styleWriter != nil {
		styleWriter.Reset()
	}

	// send any output + error for processing (e.g. adding to history)
	outputChan <- output
}

// When the user presses tab or a similar hotkey, we want to turn the
// autosuggest into a real command
func (this *ShellState) RealizeAutosuggest(buffer *ShellBuffer, sendToChild bool, colorStr string) {
	log.Printf("Realizing autosuggest: %s", this.LastAutosuggest)

	writer := this.ParentOut
	if sendToChild {
		writer = this.ChildIn
	}

	// If we're not at the end of the line, we write out the remaining command
	// before writing the autosuggest
	jumpforward := buffer.Size() - buffer.Cursor()
	if jumpforward > 0 {
		// go right for the length of the suffix
		for i := 0; i < jumpforward; i++ {
			// move cursor right
			fmt.Fprintf(writer, "\x1b[C")
			buffer.Write("\x1b[C")
		}
	}

	// set color
	if colorStr != "" {
		this.ParentOut.Write([]byte(colorStr))
	}

	// Write the autosuggest
	fmt.Fprintf(writer, "%s", this.LastAutosuggest)
	buffer.Write(this.LastAutosuggest)

	// clear the autosuggest now that we've used it
	this.LastAutosuggest = ""
}

// We have a pending autosuggest and we've just received the cursor location
// from the terminal. We can now render the autosuggest (in the greyed out
// style)
func (this *ShellState) ShowAutosuggest(
	buffer *ShellBuffer, result *AutosuggestResult, cursorCol int, termWidth int) {

	suggestion := result.Suggestion

	if suggestion == "" {
		// no suggestion
		return
	}

	// if suggestion starts with "prediction: " remove that
	// this is a dumb artifact of autosuggest few-shot learning
	const predictionPrefix = "prediction: "
	if strings.HasPrefix(suggestion, predictionPrefix) {
		suggestion = suggestion[len(predictionPrefix):]
	}

	//log.Printf("ShowAutosuggest: %s", result.Suggestion)

	if result.Command != buffer.String() {
		// this is an old result, it doesn't match the current command/prompt buffer
		log.Printf("Autosuggest result is old, ignoring. Expected: %s, got: %s", buffer.String(), result.Command)
		// TODO we can check the prefix and try to continue in this case
		return
	}

	if suggestion == this.LastAutosuggest {
		// if the suggestion is the same as the last one, ignore it
		return
	}

	if suggestion == strings.TrimSpace(buffer.String()) {
		// if the suggestion is the same as the command, ignore it
		return
	}

	// if the suggestion is multiple lines grab the first one
	if strings.Contains(suggestion, "\n") {
		suggestion = strings.Split(suggestion, "\n")[0]
	}

	if result.Command != "" {
		if strings.HasPrefix(
			strings.ToLower(suggestion), strings.ToLower(result.Command)) {
			// if the suggestion starts with the original command, remove original text
			suggestion = suggestion[len(result.Command):]
		} else if this.State == stateShell {
			// the prefix strategy is required for commands
			return
		}
	}

	// Print out autocomplete suggestion
	cmdLen := buffer.Size()
	jumpForward := cmdLen - buffer.Cursor()

	this.ClearAutosuggest(this.Color.Command)
	this.LastAutosuggest = suggestion
	this.AutosuggestBuffer = NewShellBuffer()
	this.AutosuggestBuffer.SetPromptLength(cursorCol)
	this.AutosuggestBuffer.SetTerminalWidth(termWidth)

	// Use autosuggest buffer to get the bytes to write the greyed out
	// autosuggestion and then move the cursor back to the original position
	buf := this.AutosuggestBuffer.WriteAutosuggest(
		suggestion, jumpForward, this.Color.Autosuggest)

	this.ParentOut.Write([]byte(buf))
}

// Update autosuggest when we receive new data.
// Clears the old autosuggest if necessary and requests a new one.
// If the new next matches the old autosuggest prefix then we leave it.
func (this *ShellState) RefreshAutosuggest(
	newData []byte, buffer *ShellBuffer, colorStr string) {
	// if we're typing out the exact autosuggest, and we haven't moved the cursor
	// backwards in the buffer, then we can just append and adjust the
	// autosuggest
	if buffer.Size() > 0 &&
		buffer.Size() == buffer.Cursor() &&
		bytes.HasPrefix([]byte(this.LastAutosuggest), newData) {
		this.LastAutosuggest = this.LastAutosuggest[len(newData):]
		if colorStr != "" {
			this.ParentOut.Write([]byte(colorStr))
		}
		this.AutosuggestBuffer.EatAutosuggestRune()
		return
	}

	// otherwise, clear the autosuggest
	this.ClearAutosuggest(colorStr)

	// and request a new one
	if this.State == stateShell || this.State == statePrompting {
		this.RequestAutosuggest(
			this.Butterfish.Config.ShellAutosuggestTimeout, buffer.String())
	}
}

func (this *ShellState) ClearAutosuggest(colorStr string) {
	if this.LastAutosuggest == "" || this.AutosuggestBuffer == nil {
		// there wasn't actually a last autosuggest, so nothing to clear
		return
	}

	this.LastAutosuggest = ""
	this.ParentOut.Write(this.AutosuggestBuffer.ClearLast(colorStr))
	this.AutosuggestBuffer = nil
}

func (this *ShellState) getAutosuggestEncoder() *tiktoken.Tiktoken {
	if this.AutosuggestEncoder == nil {
		modelName := this.Butterfish.Config.ShellAutosuggestModel
		encoder, err := tiktoken.EncodingForModel(modelName)
		if err != nil {
			log.Printf("Warning: Error getting encoder for autosuggest model %s: %s", modelName, err)
			encoder, err = tiktoken.GetEncoding(DEFAULT_AUTOSUGGEST_ENCODER)
			if err != nil {
				panic(fmt.Sprintf("Error getting fallback autosuggest encoder %s: %s", DEFAULT_AUTOSUGGEST_ENCODER, err))
			}
		}

		this.AutosuggestEncoder = encoder
	}

	return this.AutosuggestEncoder
}

func (this *ShellState) getPromptEncoder() *tiktoken.Tiktoken {
	if this.PromptEncoder == nil {
		modelName := this.Butterfish.Config.ShellPromptModel
		encoder, err := tiktoken.EncodingForModel(modelName)
		if err != nil {
			log.Printf("Warning: Error getting encoder for prompt model %s: %s", modelName, err)
			encoder, err = tiktoken.GetEncoding(DEFAULT_PROMPT_ENCODER)
			if err != nil {
				panic(fmt.Sprintf("Error getting fallback prompt encoder %s: %s", DEFAULT_PROMPT_ENCODER, err))
			}
		}

		this.PromptEncoder = encoder
	}

	return this.PromptEncoder
}

// rewrite this for autosuggest
func (this *ShellState) RequestAutosuggest(delay time.Duration, command string) {
	if !this.AutosuggestEnabled {
		return
	}

	if this.AutosuggestCancel != nil {
		// clear out a previous request
		this.AutosuggestCancel()
	}
	this.AutosuggestCtx, this.AutosuggestCancel = context.WithCancel(context.Background())

	// if command is only whitespace, don't bother sending it
	if len(command) > 0 && strings.TrimSpace(command) == "" {
		return
	}

	var suggestPrompt string
	var err error

	trimmed := strings.TrimSpace(command)

	if len(command) == 0 {
		// command completion when we haven't started a command
		suggestPrompt, err = this.Butterfish.PromptLibrary.GetUninterpolatedPrompt(prompt.ShellAutosuggestNewCommand)
	} else if strings.HasPrefix(trimmed, "!") || this.GoalMode {
		// goal mode prompts should autocomplete like natural language
		suggestPrompt, err = this.Butterfish.PromptLibrary.GetUninterpolatedPrompt(prompt.ShellAutosuggestPrompt)
	} else if !unicode.IsUpper(rune(command[0])) {
		// command completion when we have started typing a command
		suggestPrompt, err = this.Butterfish.PromptLibrary.GetUninterpolatedPrompt(prompt.ShellAutosuggestCommand)
	} else {
		// prompt completion, like we're asking a question
		suggestPrompt, err = this.Butterfish.PromptLibrary.GetUninterpolatedPrompt(prompt.ShellAutosuggestPrompt)
	}

	if err != nil {
		log.Printf("Error getting prompt from library: %s", err)
		return
	}

	go RequestCancelableAutosuggest(
		this.AutosuggestCtx,
		delay,
		command,
		suggestPrompt,
		this.Butterfish.LLMClient,
		this.Butterfish.Config.ShellAutosuggestModel,
		this.Butterfish.Config.Verbose > 1,
		this.History,
		this.Butterfish.Config.ShellMaxHistoryBlockTokens,
		this.AutosuggestChan,
		this.getAutosuggestEncoder())

}

// This is a function rather than a routine to isolate the concurrent steps
// taken when this is a goroutine. That's why it has so many args.
func RequestCancelableAutosuggest(
	ctx context.Context,
	delay time.Duration,
	currCommand string,
	rawPrompt string,
	llmClient LLM,
	model string,
	verbose bool,
	history *ShellHistory,
	maxHistoryBlockTokens int,
	autosuggestChan chan<- *AutosuggestResult,
	encoder *tiktoken.Tiktoken,
) {

	if delay > 0 {
		time.Sleep(delay)
	}
	if ctx.Err() != nil {
		return
	}

	totalTokens := 1600 // limit autosuggest to 1600 tokens for cost reasons
	reserveForAnswer := 64
	var err error

	historyBlocks, _ := getHistoryBlocksByTokens(history, encoder,
		maxHistoryBlockTokens, totalTokens-reserveForAnswer, 4)

	historyStr := HistoryBlocksToString(historyBlocks)
	var prmpt string

	if currCommand != "" {
		prmpt, err = prompt.Interpolate(rawPrompt,
			"history", historyStr,
			"command", currCommand)
	} else {
		prmpt, err = prompt.Interpolate(rawPrompt,
			"history", historyStr)
	}

	if err != nil {
		log.Printf("Autosuggest error: %s", err)
		return
	}

	request := &util.CompletionRequest{
		Ctx:         ctx,
		Prompt:      prmpt,
		Model:       model,
		MaxTokens:   reserveForAnswer,
		Temperature: 0.2,
		Verbose:     verbose,
	}

	response, err := llmClient.Completion(request)
	if err != nil {
		if !strings.Contains(err.Error(), "context canceled") {
			log.Printf("Autosuggest error: %s", err)
		}
		return
	}

	autoSuggest := &AutosuggestResult{
		Command:    currCommand,
		Suggestion: response.Completion,
	}
	autosuggestChan <- autoSuggest
}

// Given a PID, this function identifies all the child PIDs of the given PID
// and returns them as a slice of ints.
func countChildPids(pid int) (int, error) {
	// Get all the processes
	processes, err := ps.Processes()
	if err != nil {
		return -1, err
	}

	// Keep a set of pids, loop through and add children to the set, keep
	// looping until the set stops growing.
	pids := make(map[int]string)
	pids[pid] = "butterfish"
	for {
		// Keep track of how many pids we've added in this iteration
		added := 0

		// Loop through all the processes
		for _, process := range processes {
			// If the process is a child of one of the pids we're tracking,
			// add it to the set.
			_, childOfParent := pids[process.PPid()]
			_, alreadyAdded := pids[process.Pid()]
			if childOfParent && !alreadyAdded {
				pids[process.Pid()] = process.Executable()
				added++
			}
		}

		// If we didn't add any new pids, we're done.
		if added == 0 {
			break
		}
	}

	// subtract 1 because we don't want to count the parent pid
	totalPids := -1

	for _, process := range pids {
		switch process {
		case "sh", "bash", "zsh":
			// We want to keep butterfish on for child shells
		default:
			totalPids++
		}
	}

	return totalPids, nil
}

func HasRunningChildren() bool {
	// get this process's pid
	pid := os.Getpid()

	// get the number of child processes
	count, err := countChildPids(pid)
	if err != nil {
		log.Printf("Error counting child processes: %s", err)
		return false
	}

	if count > 0 {
		return true
	}
	return false
}
