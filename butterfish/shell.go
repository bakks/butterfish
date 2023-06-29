package butterfish

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/bakks/butterfish/prompt"
	"github.com/bakks/butterfish/util"

	"github.com/mitchellh/go-ps"
	"github.com/pkoukk/tiktoken-go"
	"golang.org/x/term"
)

const ESC_CUP = "\x1b[6n" // Request the cursor position
const ESC_UP = "\x1b[%dA"
const ESC_RIGHT = "\x1b[%dC"
const ESC_LEFT = "\x1b[%dD"
const ESC_CLEAR = "\x1b[0K"

// Special characters that we wrap the shell's command prompt in (PS1) so
// that we can detect where it starts and ends.
const PROMPT_PREFIX = "\033Q"
const PROMPT_SUFFIX = "\033R"
const PROMPT_PREFIX_ESCAPED = "\\033Q"
const PROMPT_SUFFIX_ESCAPED = "\\033R"
const EMOJI_DEFAULT = "🐠"
const EMOJI_GOAL = "🟦"
const EMOJI_GOAL_UNSAFE = "🟥"

var ps1Regex = regexp.MustCompile(" ([0-9]+)" + PROMPT_SUFFIX)
var ps1FullRegex = regexp.MustCompile(EMOJI_DEFAULT + " ([0-9]+)" + PROMPT_SUFFIX)

var DarkShellColorScheme = &ShellColorScheme{
	Prompt:           "\x1b[38;5;154m",
	PromptGoal:       "\x1b[38;5;200m",
	PromptGoalUnsafe: "\x1b[38;5;9m",
	Command:          "\x1b[0m",
	Autosuggest:      "\x1b[38;5;241m",
	Answer:           "\x1b[38;5;214m",
	GoalMode:         "\x1b[38;5;51m",
	Error:            "\x1b[38;5;196m",
}

var LightShellColorScheme = &ShellColorScheme{
	Prompt:           "\x1b[38;5;28m",
	PromptGoal:       "\x1b[38;5;200m",
	PromptGoalUnsafe: "\x1b[38;5;9m",
	Command:          "\x1b[0m",
	Autosuggest:      "\x1b[38;5;241m",
	Answer:           "\x1b[38;5;214m",
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
	default:
		return "Unknown"
	}
}

type Tokenization struct {
	InputLength int    // the unprocessed length of the pretokenized plus truncated content
	NumTokens   int    // number of tokens in the data
	Data        string // tokenized and truncated content
}

type HistoryBuffer struct {
	Type    int
	Content *ShellBuffer
	// This is to cache tokenization plus truncation of the content
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
// a slice of util.HistoryBlock objects. You can add a new block, append to
// the last block, and get the the last n bytes of the history as an array of
// HistoryBlocks.
type ShellHistory struct {
	Blocks []*HistoryBuffer
}

func NewShellHistory() *ShellHistory {
	return &ShellHistory{
		Blocks: make([]*HistoryBuffer, 0),
	}
}

func (this *ShellHistory) add(historyType int, block string) {
	buffer := NewShellBuffer()
	buffer.Write(block)
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

	numBlocks := len(this.Blocks)
	// if we have a block already, and it matches the type, append to it
	if numBlocks > 0 {
		lastBlock := this.Blocks[numBlocks-1]

		if lastBlock.Type == historyType {
			lastBlock.Content.Write(data)
			return
		}
	}

	// if the history type doesn't match we fall through and add a new block
	this.add(historyType, data)
}

func (this *ShellHistory) NewBlock() {
	length := len(this.Blocks)
	if length > 0 {
		this.add(this.Blocks[length-1].Type, "")
	}
}

// Go back in history for a certain number of bytes.
func (this *ShellHistory) GetLastNBytes(numBytes int, truncateLength int) []util.HistoryBlock {
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
			Type:    block.Type,
			Content: content,
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
	for i := len(this.Blocks) - 1; i >= 0; i-- {
		cont := cb(this.Blocks[i])
		if !cont {
			break
		}
	}
}

func (this *ShellHistory) LogRecentHistory() {
	blocks := this.GetLastNBytes(2000, 512)
	log.Printf("Recent history: =======================================")
	builder := strings.Builder{}
	for _, block := range blocks {
		builder.WriteString(fmt.Sprintf("%s: %s\n", HistoryTypeToString(block.Type), block.Content))
	}
	log.Printf(builder.String())
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
	State                   int
	GoalMode                bool
	GoalModeBuffer          string
	GoalModeGoal            string
	GoalModeUnsafe          bool
	PromptSuffixCounter     int
	ChildOutReader          chan *byteMsg
	ParentInReader          chan *byteMsg
	CursorPosChan           chan *cursorPosition
	PromptOutputChan        chan *byteMsg
	PrintErrorChan          chan error
	AutosuggestChan         chan *AutosuggestResult
	History                 *ShellHistory
	PromptAnswerWriter      io.Writer
	Prompt                  *ShellBuffer
	PromptResponseCancel    context.CancelFunc
	Command                 *ShellBuffer
	TerminalWidth           int
	Color                   *ShellColorScheme
	TokensReservedForAnswer int
	// these are used to estimate number of tokens
	AutosuggestEncoder *tiktoken.Tiktoken
	PromptEncoder      *tiktoken.Tiktoken

	// autosuggest config
	AutosuggestEnabled bool
	LastAutosuggest    string
	AutosuggestCtx     context.Context
	AutosuggestCancel  context.CancelFunc
	AutosuggestBuffer  *ShellBuffer
}

func (this *ShellState) setState(state int) {
	log.Printf("State change: %s -> %s", stateNames[this.State], stateNames[state])
	this.State = state
}

func clearByteChan(r <-chan *byteMsg, timeout time.Duration) {
	for {
		select {
		case <-time.After(timeout):
			return
		case <-r:
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
		log.Printf("Unknown shell %s, Butterfish is going to leave the PS1 alone. This means that you won't get a custom prompt in Butterfish, and Butterfish won't be able to parse the exit code of the previous command, used for centain features. Create an issue at https://github.com/bakks/butterfish.", shell)
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
// - The last exit code/status seen in the string (i.e. will be non-zero if
//   previous command failed.
// - The number of prompts identified in the string.
// - The string with the special prompt escape sequences removed.
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

	currIcon := EMOJI_DEFAULT
	if this.GoalMode {
		if this.GoalModeUnsafe {
			currIcon = EMOJI_GOAL_UNSAFE
		} else {
			currIcon = EMOJI_GOAL
		}
	}

	return ParsePS1(data, regex, currIcon)
}

func (this *ButterfishCtx) ShellMultiplexer(
	childIn io.Writer, childOut io.Reader,
	parentIn io.Reader, parentOut io.Writer) {

	this.SetPS1(childIn)

	colorScheme := DarkShellColorScheme
	if !this.Config.ShellColorDark {
		colorScheme = LightShellColorScheme
	}

	log.Printf("Starting shell multiplexer")

	childOutReader := make(chan *byteMsg, 8)
	parentInReader := make(chan *byteMsg, 8)
	// This is a buffered channel so that we don't block reading input when
	// pushing a new position
	parentPositionChan := make(chan *cursorPosition, 128)

	go readerToChannel(childOut, childOutReader)
	go readerToChannelWithPosition(parentIn, parentInReader, parentPositionChan)

	carriageReturnWriter := util.NewReplaceWriter(parentOut, "\n", "\r\n")

	termWidth, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		panic(err)
	}

	sigwinch := make(chan os.Signal, 1)
	signal.Notify(sigwinch, syscall.SIGWINCH)

	//	if this.Config.ShellPluginMode {
	//		client, err := this.StartPluginClient()
	//		if err != nil {
	//			panic(err)
	//		}
	//
	//		go client.Mux(this.Ctx)
	//	}

	shellState := &ShellState{
		Butterfish:              this,
		ParentOut:               parentOut,
		ChildIn:                 childIn,
		Sigwinch:                sigwinch,
		State:                   stateNormal,
		ChildOutReader:          childOutReader,
		ParentInReader:          parentInReader,
		CursorPosChan:           parentPositionChan,
		PrintErrorChan:          make(chan error),
		History:                 NewShellHistory(),
		PromptOutputChan:        make(chan *byteMsg),
		PromptAnswerWriter:      carriageReturnWriter,
		Command:                 NewShellBuffer(),
		Prompt:                  NewShellBuffer(),
		TerminalWidth:           termWidth,
		AutosuggestEnabled:      this.Config.ShellAutosuggestEnabled,
		AutosuggestChan:         make(chan *AutosuggestResult),
		Color:                   colorScheme,
		PromptMaxTokens:         NumTokensForModel(this.Config.ShellPromptModel),
		AutosuggestMaxTokens:    NumTokensForModel(this.Config.ShellAutosuggestModel),
		TokensReservedForAnswer: 512,
	}

	shellState.Prompt.SetTerminalWidth(termWidth)
	shellState.Prompt.SetColor(colorScheme.Prompt)

	// clear out any existing output to hide the PS1 export stuff
	clearByteChan(childOutReader, 100*time.Millisecond)
	fmt.Fprintf(childIn, "\n")

	// start
	shellState.Mux()
}

//func rgbaToColorString(r, g, b, _ uint32) string {
//	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r/255, g/255, b/255)
//}

// We expect the input string to end with a line containing "RUN: " followed by
// the command to run. If no command is found we return ""
func parseGoalModeCommand(input string) string {
	if input == "" {
		return ""
	}
	lines := strings.Split(input, "\n")

	for _, line := range lines {
		if strings.HasPrefix(line, "RUN: ") {
			return strings.TrimPrefix(line, "RUN: ")
		}
	}

	return ""
}

func (this *ShellState) Errorf(format string, args ...any) {
	this.PrintErrorChan <- fmt.Errorf(format, args...)
}

func (this *ShellState) PrintError(err error) {
	this.PrintErrorChan <- err
}

// TODO add a diagram of streams here
func (this *ShellState) Mux() {
	log.Printf("Started shell mux")
	parentInBuffer := []byte{}
	childOutBuffer := []byte{}

	for {
		select {
		case <-this.Butterfish.Ctx.Done():
			return

		case err := <-this.PrintErrorChan:
			this.History.Append(historyTypeShellOutput, err.Error())
			fmt.Fprintf(this.ParentOut, "%s%s", this.Color.Error, err.Error())

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
			log.Printf("Got SIGWINCH with new width %d", termWidth)
			this.TerminalWidth = termWidth
			this.Prompt.SetTerminalWidth(termWidth)
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
			default:
				log.Printf("Got autosuggest result in unexpected state %d", this.State)
				continue
			}

			this.ShowAutosuggest(buffer, result, col-1, this.TerminalWidth)

		// We finished with prompt output response, go back to normal mode
		case output := <-this.PromptOutputChan:
			this.History.Append(historyTypeLLMOutput, string(output.Data))

			// If there is child output waiting to be printed, print that now
			if len(childOutBuffer) > 0 {
				this.ParentOut.Write(childOutBuffer)
				this.History.Append(historyTypeShellOutput, string(childOutBuffer))
				childOutBuffer = []byte{}
			}

			// Get a new prompt
			this.ChildIn.Write([]byte("\n"))

			if this.GoalMode {
				llmAsk := string(output.Data)
				if strings.Contains(llmAsk, "GOAL ACHIEVED") {
					log.Printf("Goal mode: goal achieved, exiting")
					fmt.Fprintf(this.PromptAnswerWriter, "%sExited goal mode.%s\n", this.Color.Answer, this.Color.Command)
					this.GoalMode = false
					this.setState(stateNormal)
					continue
				}
				if strings.Contains(llmAsk, "GOAL FAILED") {
					log.Printf("Goal mode: goal failed, exiting")
					fmt.Fprintf(this.PromptAnswerWriter, "%sExited goal mode.%s\n", this.Color.Answer, this.Color.Command)
					this.GoalMode = false
					this.setState(stateNormal)
					continue
				}

				goalModeCmd := parseGoalModeCommand(llmAsk)
				if goalModeCmd != "" {
					// Execute the given command on the local shell
					log.Printf("Goal mode: running command: %s", goalModeCmd)
					this.GoalModeBuffer = ""
					this.PromptSuffixCounter = 0
					this.setState(stateNormal)
					fmt.Fprintf(this.ChildIn, "%s", goalModeCmd)
					if this.GoalModeUnsafe {
						fmt.Fprintf(this.ChildIn, "\n")
					}
					continue
				}

				this.PromptSuffixCounter = -10000
			}

			this.RequestAutosuggest(0, "")
			this.setState(stateNormal)

		case childOutMsg := <-this.ChildOutReader:
			if childOutMsg == nil {
				log.Println("Child out reader closed")
				this.Butterfish.Cancel()
				return
			}

			//log.Printf("Got child output:\n%x", childOutMsg.Data)

			lastStatus, prompts, childOutStr := this.ParsePS1(string(childOutMsg.Data))
			//			if prompts != 0 {
			//				log.Printf("Child exited with status %d", lastStatus)
			//			}
			this.PromptSuffixCounter += prompts

			// If we're actively printing a response we buffer child output
			if this.State == statePromptResponse {
				childOutBuffer = append(childOutBuffer, childOutMsg.Data...)
				continue
			}

			if this.GoalMode {
				this.GoalModeBuffer += childOutStr
			}

			// If we're getting child output while typing in a shell command, this
			// could mean the user is paging through old commands, or doing a tab
			// completion, or something unknown, so we don't want to add to history.
			if this.State != stateShell {
				this.History.Append(historyTypeShellOutput, childOutStr)
			}
			this.ParentOut.Write([]byte(childOutStr))

			if this.GoalMode && this.PromptSuffixCounter >= 2 {
				// move cursor to the beginning of the line and clear the line
				fmt.Fprintf(this.ParentOut, "\r%s", ESC_CLEAR)
				this.GoalModeCommandResponse(lastStatus, this.GoalModeBuffer)
				this.GoalModeBuffer = ""
				this.PromptSuffixCounter = 0
			}

		case parentInMsg := <-this.ParentInReader:
			if parentInMsg == nil {
				log.Println("Parent in reader closed")
				this.Butterfish.Cancel()
				return
			}

			data := parentInMsg.Data

			// include any cached data
			if len(parentInBuffer) > 0 {
				data = append(parentInBuffer, data...)
				parentInBuffer = []byte{}
			}

			// If we've started an ANSI escape sequence, it might not be complete
			// yet, so we need to cache it and wait for the next message
			if incompleteAnsiSequence(data) {
				parentInBuffer = append(parentInBuffer, data...)
				continue
			}

			for {
				// The InputFromParent function consumes bytes from the passed in data
				// buffer and returns unprocessed bytes, so we loop and continue to
				// pass data in, if available
				leftover := this.InputFromParent(this.Butterfish.Ctx, data)

				if leftover == nil || len(leftover) == 0 {
					break
				}
				if len(leftover) == len(data) {
					// nothing was consumed, we buffer and try again later
					parentInBuffer = append(parentInBuffer, leftover...)
					break
				}

				// go again with the leftover data
				data = leftover
			}
		}
	}
}

func (this *ShellState) InputFromParent(ctx context.Context, data []byte) []byte {
	hasCarriageReturn := bytes.Contains(data, []byte{'\r'})

	switch this.State {
	case statePromptResponse:
		// Ctrl-C while receiving prompt
		if data[0] == 0x03 {
			this.PromptResponseCancel()
			this.PromptResponseCancel = nil
			return data[1:]
		}

		// If we're in the middle of a prompt response we ignore all other input
		return data

	case stateNormal:
		if HasRunningChildren() {
			// If we have running children then the shell is running something,
			// so just forward the input.
			this.ChildIn.Write(data)
			return nil
		}

		if data[0] == 0x03 && this.GoalMode {
			// Ctrl-C while in goal mode
			fmt.Fprintf(this.PromptAnswerWriter, "\n%sExited goal mode.%s\n", this.Color.Answer, this.Color.Command)
			this.GoalMode = false
			this.setState(stateNormal)
		}

		// Check if the first character is uppercase or a bang
		// TODO handle the case where this input is more than a single character, contains other stuff like carriage return, etc
		if unicode.IsUpper(rune(data[0])) || data[0] == '!' {
			this.setState(statePrompting)
			this.Prompt.Clear()
			this.Prompt.Write(string(data))

			// Write the actual prompt start
			color := this.Color.Prompt
			if data[0] == '!' {
				color = this.Color.PromptGoal
			}
			this.Prompt.SetColor(color)
			fmt.Fprintf(this.ParentOut, "%s%s", color, data)

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
				this.ChildIn.Write(data)
			}
			return data[1:]

		} else if data[0] == '\r' {
			this.ChildIn.Write(data)
			return data[1:]

		} else {
			this.Command = NewShellBuffer()
			this.Command.Write(string(data))

			if this.Command.Size() > 0 {
				// this means that the command is not empty, i.e. the input wasn't
				// some control character
				this.RefreshAutosuggest(data, this.Command, this.Color.Command)
				this.setState(stateShell)
			} else {
				this.ClearAutosuggest(this.Color.Command)
			}

			this.ParentOut.Write([]byte(this.Color.Command))
			this.ChildIn.Write(data)
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
			toPrint := this.Prompt.Clear()
			this.ParentOut.Write(toPrint)
			this.ParentOut.Write([]byte(this.Color.Command))
			this.setState(stateNormal)

		} else { // otherwise user is typing a prompt
			toPrint := this.Prompt.Write(string(data))
			this.ParentOut.Write(toPrint)
			this.RefreshAutosuggest(data, this.Prompt, this.Color.Command)

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
			this.ChildIn.Write(data[:index+1])
			this.History.Append(historyTypeShellInput, this.Command.String())
			this.Command = NewShellBuffer()

			return data[index+1:]

		} else if data[0] == '\t' { // user is asking to fill in an autosuggest
			// Tab was pressed, fill in lastAutosuggest
			if this.LastAutosuggest != "" {
				this.RealizeAutosuggest(this.Command, true, this.Color.Command)
			} else {
				// no last autosuggest found, just forward the tab
				this.ChildIn.Write(data)
			}
			return data[1:]

		} else { // otherwise user is typing a command
			this.Command.Write(string(data))
			this.RefreshAutosuggest(data, this.Command, this.Color.Command)
			this.ChildIn.Write(data)
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
		this.PromptOutputChan <- &byteMsg{Data: []byte(data)}
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
	fmt.Fprintf(this.PromptAnswerWriter, "%sGoal mode starting...%s\n", this.Color.Answer, this.Color.Command)
	this.GoalModeGoal = goal
	this.Prompt.Clear()

	prompt := "Start now."
	log.Printf("Starting goal mode: %s", this.GoalModeGoal)
	this.History.Append(historyTypePrompt, prompt)

	this.goalModePrompt(prompt)
}

func (this *ShellState) GoalModeChat() {
	prompt := this.Prompt.String()
	this.Prompt.Clear()

	log.Printf("Goal mode chat: %s\n", prompt)
	this.goalModePrompt(prompt)
}

func (this *ShellState) GoalModeCommandResponse(status int, output string) {
	log.Printf("Goal mode response: %d\n", status)
	prompt := fmt.Sprintf("%s\nExit code: %d\n", output, status)
	this.goalModePrompt(prompt)
}

func (this *ShellState) goalModePrompt(lastPrompt string) {
	this.setState(statePromptResponse)
	requestCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	this.PromptResponseCancel = cancel

	sysMsg, err := this.Butterfish.PromptLibrary.GetPrompt(prompt.GoalModeSystemMessage,
		"goal", this.GoalModeGoal)
	if err != nil {
		msg := fmt.Errorf("ERROR: could not retrieve prompting system message: %s", err)
		log.Println(msg)
		this.PrintError(msg)
		return
	}

	lastPrompt, historyBlocks, err := this.AssembleChat(lastPrompt, sysMsg)
	if err != nil {
		this.PrintError(err)
		return
	}

	request := &util.CompletionRequest{
		Ctx:           requestCtx,
		Prompt:        lastPrompt,
		Model:         this.Butterfish.Config.ShellPromptModel,
		MaxTokens:     2048,
		Temperature:   0.8,
		HistoryBlocks: historyBlocks,
		SystemMessage: sysMsg,
	}

	// we run this in a goroutine so that we can still receive input
	// like Ctrl-C while waiting for the response
	go CompletionRoutine(request, this.Butterfish.LLMClient,
		this.PromptAnswerWriter, this.PromptOutputChan,
		this.Color.GoalMode, this.Color.Error)
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
// exceed it.
func countAndTruncate(data string, encoder *tiktoken.Tiktoken, maxTokens int) (int, string, bool) {
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
func (this *ShellState) AssembleChat(prompt, sysMsg string) (string, []util.HistoryBlock, error) {
	// How many tokens can this model handle
	totalTokens := this.PromptMaxTokens
	reserveForAnswer := this.TokensReservedForAnswer // leave available
	maxPromptTokens := 512                           // for the prompt specifically
	// for each individual history block
	maxHistoryBlockTokens := this.Butterfish.Config.ShellMaxHistoryBlockTokens
	// How much for the total request (prompt, history, sys msg)
	maxCombinedPromptTokens := totalTokens - reserveForAnswer

	return assembleChat(prompt, sysMsg, this.History,
		this.Butterfish.Config.ShellPromptModel, this.getPromptEncoder(),
		maxPromptTokens, maxHistoryBlockTokens, maxCombinedPromptTokens)
}

// Build a list of HistoryBlocks for use in GPT chat history, and ensure the
// prompt and system message plus the history are within the token limit.
// The prompt may be truncated based on maxPromptTokens.
func assembleChat(
	prompt, sysMsg string, history *ShellHistory, model string, encoder *tiktoken.Tiktoken,
	maxPromptTokens, maxHistoryBlockTokens, maxTokens int) (string, []util.HistoryBlock, error) {

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
		return "", nil, fmt.Errorf("System message too long, %d tokens", sysMsgTokens)
	}

	blocks, historyTokens := getHistoryBlocksByTokens(history, encoder,
		maxHistoryBlockTokens, maxTokens-usedTokens, tokensPerMessage)
	usedTokens += historyTokens

	log.Printf("Chat tokens: %d\n", usedTokens)
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
func getHistoryBlocksByTokens(history *ShellHistory, encoder *tiktoken.Tiktoken, maxHistoryBlockTokens, maxTokens, tokensPerMessage int) ([]util.HistoryBlock, int) {

	blocks := []util.HistoryBlock{}
	usedTokens := 0

	history.IterateBlocks(func(block *HistoryBuffer) bool {
		msgTokens := tokensPerMessage

		var roleString string
		if block.Type == historyTypeLLMOutput {
			roleString = "assistant"
		} else {
			roleString = "user"
		}

		// add tokens for role
		msgTokens += len(encoder.Encode(roleString, nil, nil))
		contentStr := block.Content.String()

		// check existing block tokenizations
		content, contentTokens, ok := block.GetTokenization(encoder.EncoderName(), block.Content.Size())

		if !ok { // cache miss
			// avoid processing super long strings with a ceiling
			ceiling := maxHistoryBlockTokens * 4
			contentLen := len(contentStr)
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
			Type:    block.Type,
			Content: content,
		}

		// we prepend the block so that the history is in the correct order
		blocks = append([]util.HistoryBlock{newBlock}, blocks...)
		return true
	})

	return blocks, usedTokens
}

func (this *ShellState) SendPrompt() {
	this.setState(statePromptResponse)

	requestCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	this.PromptResponseCancel = cancel

	sysMsg, err := this.Butterfish.PromptLibrary.GetPrompt(prompt.PromptShellSystemMessage)
	if err != nil {
		msg := fmt.Errorf("ERROR: could not retrieve prompting system message: %s", err)
		log.Println(msg)
		this.PrintError(msg)
		return
	}

	prompt := this.Prompt.String()
	prompt, historyBlocks, err := this.AssembleChat(prompt, sysMsg)
	if err != nil {
		this.PrintError(err)
		return
	}

	request := &util.CompletionRequest{
		Ctx:           requestCtx,
		Prompt:        prompt,
		Model:         this.Butterfish.Config.ShellPromptModel,
		MaxTokens:     this.TokensReservedForAnswer,
		Temperature:   0.7,
		HistoryBlocks: historyBlocks,
		SystemMessage: sysMsg,
	}

	this.History.Append(historyTypePrompt, this.Prompt.String())

	// we run this in a goroutine so that we can still receive input
	// like Ctrl-C while waiting for the response
	go CompletionRoutine(request, this.Butterfish.LLMClient,
		this.PromptAnswerWriter, this.PromptOutputChan,
		this.Color.Answer, this.Color.Error)

	this.Prompt.Clear()
}

func CompletionRoutine(request *util.CompletionRequest, client LLM, writer io.Writer, outputChan chan *byteMsg, normalColor, errorColor string) {
	fmt.Fprintf(writer, "%s", normalColor)
	output, err := client.CompletionStream(request, writer)

	toSend := []byte{}
	if output != "" {
		toSend = []byte(output)
	}

	if err != nil {
		errStr := fmt.Sprintf("Error prompting LLM: %s\n", err)

		// This error means the user needs to set up a subscription, give advice
		if strings.Contains(errStr, ERR_429) {
			errStr = fmt.Sprintf("%s\n%s", errStr, ERR_429_HELP)
		}

		log.Printf("%s", errStr)

		if !strings.Contains(errStr, "context canceled") {
			fmt.Fprintf(writer, "%s%s", errorColor, errStr)
			// We want to put the error message in the history as well
			toSend = append(toSend, []byte(errStr)...)
		}
	}

	if len(toSend) > 0 {
		// send any output + error for processing (e.g. adding to history)
		outputChan <- &byteMsg{Data: toSend}
	}
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

	if result.Suggestion == "" {
		// no suggestion
		return
	}

	//log.Printf("ShowAutosuggest: %s", result.Suggestion)

	if result.Command != buffer.String() {
		// this is an old result, it doesn't match the current command buffer
		log.Printf("Autosuggest result is old, ignoring. Expected: %s, got: %s", buffer.String(), result.Command)
		return
	}

	if strings.Contains(result.Suggestion, "\n") {
		// if result.Suggestion has newlines then discard it
		return
	}

	if result.Suggestion == this.LastAutosuggest {
		// if the suggestion is the same as the last one, ignore it
		return
	}

	if result.Command != "" &&
		!strings.HasPrefix(
			strings.ToLower(result.Suggestion),
			strings.ToLower(result.Command)) {
		// test that the command is equal to the beginning of the suggestion
		log.Printf("Autosuggest result is invalid, ignoring")
		return
	}

	if result.Suggestion == buffer.String() {
		// if the suggestion is the same as the command, ignore it
		return
	}

	// Print out autocomplete suggestion
	cmdLen := buffer.Size()
	suggToAdd := result.Suggestion[cmdLen:]
	jumpForward := cmdLen - buffer.Cursor()

	this.LastAutosuggest = suggToAdd

	this.AutosuggestBuffer = NewShellBuffer()
	this.AutosuggestBuffer.SetPromptLength(cursorCol)
	this.AutosuggestBuffer.SetTerminalWidth(termWidth)

	// Use autosuggest buffer to get the bytes to write the greyed out
	// autosuggestion and then move the cursor back to the original position
	buf := this.AutosuggestBuffer.WriteAutosuggest(suggToAdd, jumpForward, this.Color.Autosuggest)

	this.ParentOut.Write([]byte(buf))
}

// Update autosuggest when we receive new data
func (this *ShellState) RefreshAutosuggest(newData []byte, buffer *ShellBuffer, colorStr string) {
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
	if this.LastAutosuggest == "" {
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
		encodingName := tiktoken.MODEL_TO_ENCODING[modelName]
		if encodingName == "" {
			log.Printf("WARNING: Encoder for autosuggest model %s not found, using default", modelName)
			encodingName = "text-davinci-003"
		}

		encoder, err := tiktoken.GetEncoding(encodingName)
		if err != nil {
			panic(fmt.Sprintf("Error getting encoder for autosuggest model %s: %s", modelName, err))
		}

		this.AutosuggestEncoder = encoder
	}

	return this.AutosuggestEncoder
}

func (this *ShellState) getPromptEncoder() *tiktoken.Tiktoken {
	if this.PromptEncoder == nil {
		modelName := this.Butterfish.Config.ShellPromptModel
		encodingName := tiktoken.MODEL_TO_ENCODING[modelName]
		if encodingName == "" {
			log.Printf("WARNING: Encoder for prompt model %s not found, using default", modelName)
			encodingName = "gpt-3.5"
		}

		encoder, err := tiktoken.GetEncoding(encodingName)
		if err != nil {
			panic(fmt.Sprintf("Error getting encoder for prompt model %s: %s", modelName, err))
		}

		this.PromptEncoder = encoder
	}

	return this.PromptEncoder
}

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

	maxTokensPerHistoryBlock := this.Butterfish.Config.ShellMaxHistoryBlockTokens
	maxTokens := 1500 // Send a total of 1500 history tokens if available

	historyBlocks, _ := getHistoryBlocksByTokens(this.History, this.getAutosuggestEncoder(),
		maxTokensPerHistoryBlock, maxTokens, 4)

	historyStr := HistoryBlocksToString(historyBlocks)
	var llmPrompt string
	var err error

	if len(command) == 0 {
		// command completion when we haven't started a command
		llmPrompt, err = this.Butterfish.PromptLibrary.GetPrompt(prompt.PromptShellAutosuggestNewCommand,
			"history", historyStr)
	} else if !unicode.IsUpper(rune(command[0])) {
		// command completion when we have started typing a command
		llmPrompt, err = this.Butterfish.PromptLibrary.GetPrompt(prompt.PromptShellAutosuggestCommand,
			"history", historyStr,
			"command", command)
	} else {
		// prompt completion, like we're asking a question
		llmPrompt, err = this.Butterfish.PromptLibrary.GetPrompt(prompt.PromptShellAutosuggestPrompt,
			"history", historyStr,
			"command", command)
	}

	if err != nil {
		log.Printf("Error getting prompt from library: %s", err)
		return
	}

	go RequestCancelableAutosuggest(
		this.AutosuggestCtx, delay,
		command, llmPrompt,
		this.Butterfish.LLMClient,
		this.Butterfish.Config.ShellAutosuggestModel,
		this.AutosuggestChan)
}

func RequestCancelableAutosuggest(
	ctx context.Context,
	delay time.Duration,
	currCommand string,
	prompt string,
	llmClient LLM,
	model string,
	autosuggestChan chan<- *AutosuggestResult) {

	if delay > 0 {
		time.Sleep(delay)
	}
	if ctx.Err() != nil {
		return
	}

	request := &util.CompletionRequest{
		Ctx:         ctx,
		Prompt:      prompt,
		Model:       model,
		MaxTokens:   256,
		Temperature: 0.7,
	}

	output, err := llmClient.Completion(request)
	if err != nil && !strings.Contains(err.Error(), "context canceled") {
		log.Printf("Autosuggest error: %s", err)
		if strings.Contains(err.Error(), ERR_429) {
			log.Printf(ERR_429_HELP)
		}
		return
	}

	// Clean up wrapping whitespace
	output = strings.TrimSpace(output)

	// if output is wrapped in quotes, remove quotes
	if len(output) > 1 && output[0] == '"' && output[len(output)-1] == '"' {
		output = output[1 : len(output)-1]
	}

	// Clean up wrapping whitespace
	output = strings.TrimSpace(output)

	autoSuggest := &AutosuggestResult{
		Command:    currCommand,
		Suggestion: output,
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
	pids := make(map[int]bool)
	pids[pid] = true
	for {
		// Keep track of how many pids we've added in this iteration
		added := 0

		// Loop through all the processes
		for _, p := range processes {
			// If the process is a child of one of the pids we're tracking,
			// add it to the set.
			if pids[p.PPid()] && !pids[p.Pid()] {
				pids[p.Pid()] = true
				added++
			}
		}

		// If we didn't add any new pids, we're done.
		if added == 0 {
			break
		}
	}

	// subtract 1 because we don't want to count the parent pid
	return len(pids) - 1, nil
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

	// we expect 1 child because the shell is running
	if count > 1 {
		return true
	}
	return false
}
