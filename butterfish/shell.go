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
	"github.com/charmbracelet/lipgloss"
	"github.com/mitchellh/go-ps"
	"golang.org/x/term"
)

const ERR_429 = "429:insufficient_quota"
const ERR_429_HELP = "You are likely using a free OpenAI account without a subscription activated, this error means you are out of credits. To resolve it, set up a subscription at https://platform.openai.com/account/billing/overview. This requires a credit card and payment, run `butterfish help` for guidance on managing cost."

// compile a regex that matches \x1b[%d;%dR
var cursorPosRegex = regexp.MustCompile(`\x1b\[(\d+);(\d+)R`)

// Search for an ANSI cursor position sequence, e.g. \x1b[4;14R, and return:
// - row
// - column
// - length of the sequence
// - whether the sequence was found
func parseCursorPos(data []byte) (int, int, int, bool) {
	matches := cursorPosRegex.FindSubmatch(data)
	if len(matches) != 3 {
		return -1, -1, -1, false
	}
	row, err := strconv.Atoi(string(matches[1]))
	if err != nil {
		return -1, -1, -1, false
	}
	col, err := strconv.Atoi(string(matches[2]))
	if err != nil {
		return -1, -1, -1, false
	}
	return row, col, len(matches[0]), true
}

func RunShell(ctx context.Context, config *ButterfishConfig, shell string) error {
	envVars := []string{"BUTTERFISH_SHELL=1"}

	ptmx, ptyCleanup, err := ptyCommand(ctx, envVars, []string{shell})
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

// This holds a buffer that represents a tty shell buffer. Incoming data
// manipulates the buffer, for example the left arrow will move the cursor left,
// a backspace would erase the end of the buffer.
type ShellBuffer struct {
	// The buffer itself
	buffer       []rune
	cursor       int
	termWidth    int
	promptLength int
	color        string

	lastAutosuggestLen int
	lastJumpForward    int
}

func (this *ShellBuffer) SetColor(color string) {
	this.color = color
}

func (this *ShellBuffer) Clear() []byte {
	for i := 0; i < len(this.buffer); i++ {
		this.buffer[i] = ' '
	}

	originalCursor := this.cursor
	this.cursor = 0
	update := this.calculateShellUpdate(originalCursor)

	this.buffer = make([]rune, 0)

	return update
}

func (this *ShellBuffer) SetPromptLength(promptLength int) {
	this.promptLength = promptLength
}

func (this *ShellBuffer) SetTerminalWidth(width int) {
	this.termWidth = width
}

func (this *ShellBuffer) Size() int {
	return len(this.buffer)
}

func (this *ShellBuffer) Cursor() int {
	return this.cursor
}

func (this *ShellBuffer) Write(data string) []byte {
	if len(data) == 0 {
		return []byte{}
	}

	startingCursor := this.cursor
	runes := []rune(data)

	for i := 0; i < len(runes); i++ {

		if len(runes) >= i+3 && runes[i] == 0x1b && runes[i+1] == 0x5b {
			lastRune := runes[i+2]
			switch lastRune {
			case 0x41, 0x42:
				// up or down arrow, ignore these because they will break the editing line
				i += 2
				continue

			case 0x43:
				// right arrow
				if this.cursor < len(this.buffer) {
					this.cursor++
				}
				i += 2
				continue

			case 0x44:
				// left arrow
				if this.cursor > 0 {
					this.cursor--
				}
				i += 2
				continue
			}
		}

		r := rune(runes[i])

		switch r {
		case 0x08, 0x7f: // backspace
			if this.cursor > 0 && len(this.buffer) > 0 {
				this.buffer = append(this.buffer[:this.cursor-1], this.buffer[this.cursor:]...)
				this.cursor--
			}

		default:
			if this.cursor == len(this.buffer) {
				this.buffer = append(this.buffer, r)
			} else {
				this.buffer = append(this.buffer[:this.cursor], append([]rune{r}, this.buffer[this.cursor:]...)...)
			}
			this.cursor++

		}
	}

	//log.Printf("Buffer update, cursor: %d, buffer: %s, written: %s  %x", this.cursor, string(this.buffer), data, []byte(data))

	return this.calculateShellUpdate(startingCursor)
}

func (this *ShellBuffer) calculateShellUpdate(startingCursor int) []byte {
	// Ok, we've updated the buffer. Now we need to figure out what to print.
	// The assumption here is that we need to print new stuff, that might fill
	// multiple lines, might start with a prompt (i.e. not at column 0), and
	// the cursor might be in the middle of the buffer.

	var w io.Writer
	// create writer to a string buffer
	var buf bytes.Buffer
	w = &buf

	// if we have no termwidth we just print out, don't worry about wrapping
	if this.termWidth == 0 {
		// go left from the starting cursor
		fmt.Fprintf(w, "\x1b[%dD", startingCursor)
		// print the buffer
		fmt.Fprintf(w, "%s", string(this.buffer))
		// go back to the ending cursor
		fmt.Fprintf(w, "\x1b[%dD", len(this.buffer)-this.cursor)

		return buf.Bytes()
	}

	newNumLines := (max(len(this.buffer), this.cursor+1) + this.promptLength) / this.termWidth
	oldCursorLine := (startingCursor + this.promptLength) / this.termWidth
	newCursorLine := (this.cursor + this.promptLength) / this.termWidth
	newColumn := (this.cursor + this.promptLength) % this.termWidth
	posAfterWriting := (len(this.buffer) + this.promptLength) % this.termWidth

	// get cursor back to the beginning of the prompt
	// carriage return to go to left side of term
	w.Write([]byte{'\r'})
	// go up for the number of lines
	if oldCursorLine > 0 {
		// in this case we clear out the final old line
		fmt.Fprintf(w, "\x1b[0K")
		fmt.Fprintf(w, "\x1b[%dA", oldCursorLine)
	}
	// go right for the prompt length
	if this.promptLength > 0 {
		fmt.Fprintf(w, "\x1b[%dC", this.promptLength)
	}

	// set the terminal color
	if this.color != "" {
		w.Write([]byte(this.color))
	}

	// write the full new buffer
	w.Write([]byte(string(this.buffer)))

	if posAfterWriting == 0 {
		// if we are at the beginning of a new line, we need to go down
		// one line to get to the right spot
		w.Write([]byte("\r\n"))
	}

	// clear to end of line
	w.Write([]byte("\x1b[0K"))

	// if the cursor is not at the end of the buffer we need to adjust it because
	// we rewrote the entire buffer
	if this.cursor < len(this.buffer) {
		// carriage return to go to left side of term
		w.Write([]byte{'\r'})
		// go up for the number of lines
		if newNumLines-newCursorLine > 0 {
			fmt.Fprintf(w, "\x1b[%dA", newNumLines-newCursorLine)
		}
		// go right to the new cursor column
		if newColumn > 0 {
			fmt.Fprintf(w, "\x1b[%dC", newColumn)
		}
	}

	return buf.Bytes()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (this *ShellBuffer) String() string {
	return string(this.buffer)
}

func NewShellBuffer() *ShellBuffer {
	return &ShellBuffer{
		buffer: make([]rune, 0),
		cursor: 0,
	}
}

// >>> command
//            ^ cursor
// autosuggest: " foobar"
// jumpForward: 0
//
// >>> command
//          ^ cursor
// autosuggest: " foobar"
// jumpForward: 2
func (this *ShellBuffer) WriteAutosuggest(autosuggestText string, jumpForward int, colorStr string) []byte {
	// promptlength represents the starting cursor position in this context

	var w io.Writer
	// create writer to a string buffer
	var buf bytes.Buffer
	w = &buf

	numLines := (len(autosuggestText) + jumpForward + this.promptLength) / this.termWidth
	this.lastAutosuggestLen = len(autosuggestText)
	this.lastJumpForward = jumpForward

	//log.Printf("Applying autosuggest, numLines: %d, jumpForward: %d, promptLength: %d, autosuggestText: %s", numLines, jumpForward, this.promptLength, autosuggestText)

	// if we would have to jump down to the next line to write the autosuggest
	if this.promptLength+jumpForward > this.termWidth {
		// don't handle this case
		return []byte{}
	}

	// go right to the jumpForward position
	if jumpForward > 0 {
		fmt.Fprintf(w, "\x1b[%dC", jumpForward)
	}

	// handle color
	if colorStr != "" {
		w.Write([]byte(colorStr))
	} else if this.color != "" {
		w.Write([]byte(this.color))
	}

	// write the autosuggest text
	w.Write([]byte(autosuggestText))

	// return cursor to original position

	// carriage return to go to left side of term
	w.Write([]byte{'\r'})

	// go up for the number of lines
	if numLines > 0 {
		fmt.Fprintf(w, "\x1b[%dA", numLines)
	}

	// go right for the prompt length
	if this.promptLength > 0 {
		fmt.Fprintf(w, "\x1b[%dC", this.promptLength)
	}

	return buf.Bytes()
}

func (this *ShellBuffer) ClearLast(colorStr string) []byte {
	//log.Printf("Clearing last autosuggest, lastAutosuggestLen: %d, lastJumpForward: %d, promptLength: %d", this.lastAutosuggestLen, this.lastJumpForward, this.promptLength)
	buf := make([]byte, this.lastAutosuggestLen)
	for i := 0; i < this.lastAutosuggestLen; i++ {
		buf[i] = ' '
	}

	return this.WriteAutosuggest(string(buf), this.lastJumpForward, colorStr)
}

func (this *ShellBuffer) EatAutosuggestRune() {
	if this.lastJumpForward > 0 {
		panic("jump forward should be 0")
	}

	this.lastAutosuggestLen--
	this.promptLength++
}

const (
	historyTypePrompt = iota
	historyTypeShellInput
	historyTypeShellOutput
	historyTypeLLMOutput
)

type HistoryBuffer struct {
	Type    int
	Content *ShellBuffer
}

// ShellHistory keeps a record of past shell history and LLM interaction in
// a slice of util.HistoryBlock objects. You can add a new block, append to
// the last block, and get the the last n bytes of the history as an array of
// HistoryBlocks.
type ShellHistory struct {
	Blocks         []HistoryBuffer
	TruncateLength int
}

func NewShellHistory() *ShellHistory {
	return &ShellHistory{
		Blocks:         make([]HistoryBuffer, 0),
		TruncateLength: 512,
	}
}

func (this *ShellHistory) Add(historyType int, block string) {
	buffer := NewShellBuffer()
	buffer.Write(block)
	this.Blocks = append(this.Blocks, HistoryBuffer{
		Type:    historyType,
		Content: buffer,
	})
}

func (this *ShellHistory) Append(historyType int, data string) {
	length := len(this.Blocks)
	// if we have a block already, and it matches the type, append to it
	if length > 0 {
		lastBlock := this.Blocks[len(this.Blocks)-1]

		if lastBlock.Type == historyType {
			if lastBlock.Content.Size() < this.TruncateLength {
				// we append to the last block if we haven't hit the truncation length
				this.Blocks[length-1].Content.Write(data)
			}
			// if we hit the truncation length we drop the data
			return
		}
	}

	// if the history type doesn't match we fall through and add a new block
	this.Add(historyType, data)
}

func (this *ShellHistory) NewBlock() {
	length := len(this.Blocks)
	if length > 0 {
		this.Add(this.Blocks[length-1].Type, "")
	}
}

// Go back in history for a certain number of bytes.
// This truncates each block content to a maximum of 512 bytes.
func (this *ShellHistory) GetLastNBytes(numBytes int) []util.HistoryBlock {
	var blocks []util.HistoryBlock

	for i := len(this.Blocks) - 1; i >= 0 && numBytes > 0; i-- {
		block := this.Blocks[i]
		content := sanitizeTTYString(block.Content.String())
		if len(content) > this.TruncateLength {
			content = content[:this.TruncateLength]
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

func (this *ShellHistory) LogRecentHistory() {
	blocks := this.GetLastNBytes(2000)
	log.Printf("Recent history: =======================================")
	for _, block := range blocks {
		if block.Type == historyTypePrompt {
			log.Printf("Prompt: %s", block.Content)
		} else if block.Type == historyTypeShellInput {
			log.Printf("Shell input: %s", block.Content)
		} else if block.Type == historyTypeShellOutput {
			log.Printf("Shell output: %s", block.Content)
		} else if block.Type == historyTypeLLMOutput {
			log.Printf("LLM output: %s", block.Content)
		}
	}
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

type AutosuggestResult struct {
	Command    string
	Suggestion string
}

type ShellState struct {
	Butterfish *ButterfishCtx
	ParentOut  io.Writer
	ChildIn    io.Writer
	Sigwinch   chan os.Signal

	// The current state of the shell
	State                int
	ChildOutReader       chan *byteMsg
	ParentInReader       chan *byteMsg
	PromptOutputChan     chan *byteMsg
	AutosuggestChan      chan *AutosuggestResult
	History              *ShellHistory
	PromptAnswerWriter   io.Writer
	Prompt               *ShellBuffer
	PromptStyle          lipgloss.Style
	PromptResponseCancel context.CancelFunc
	Command              *ShellBuffer
	TerminalWidth        int

	PromptColorString      string
	CommandColorString     string
	AutosuggestColorString string
	AnswerColorString      string

	AutosuggestEnabled bool
	LastAutosuggest    string
	AutosuggestCtx     context.Context
	AutosuggestCancel  context.CancelFunc
	AutosuggestStyle   lipgloss.Style
	AutosuggestBuffer  *ShellBuffer
	PendingAutosuggest *AutosuggestResult
}

func (this *ButterfishCtx) ShellMultiplexer(
	childIn io.Writer, childOut io.Reader,
	parentIn io.Reader, parentOut io.Writer) {

	promptColor := "\x1b[38;5;154m"
	commandColor := "\x1b[0m"
	autosuggestColor := "\x1b[38;5;241m"
	answerColor := "\x1b[38;5;214m"

	log.Printf("Starting shell multiplexer")

	childOutReader := make(chan *byteMsg)
	parentInReader := make(chan *byteMsg)

	go readerToChannel(childOut, childOutReader)
	go readerToChannel(parentIn, parentInReader)

	promptOutputWriter := util.NewColorWriter(parentOut, answerColor)
	cleanedWriter := util.NewReplaceWriter(promptOutputWriter, "\n", "\r\n")

	termWidth, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		panic(err)
	}

	sigwinch := make(chan os.Signal, 1)
	signal.Notify(sigwinch, syscall.SIGWINCH)

	shellState := &ShellState{
		Butterfish:         this,
		ParentOut:          parentOut,
		ChildIn:            childIn,
		Sigwinch:           sigwinch,
		State:              stateNormal,
		ChildOutReader:     childOutReader,
		ParentInReader:     parentInReader,
		History:            NewShellHistory(),
		PromptOutputChan:   make(chan *byteMsg),
		PromptAnswerWriter: cleanedWriter,
		PromptStyle:        this.Config.Styles.Question,
		Command:            NewShellBuffer(),
		Prompt:             NewShellBuffer(),
		TerminalWidth:      termWidth,
		AutosuggestEnabled: this.Config.ShellAutosuggestEnabled,
		AutosuggestChan:    make(chan *AutosuggestResult),
		AutosuggestStyle:   this.Config.Styles.Grey,

		PromptColorString:      promptColor,
		CommandColorString:     commandColor,
		AutosuggestColorString: autosuggestColor,
		AnswerColorString:      answerColor,
	}

	shellState.Prompt.SetTerminalWidth(termWidth)
	shellState.Prompt.SetColor(promptColor)
	log.Printf("Prompt color: %s", shellState.PromptColorString[1:])
	log.Printf("Autosuggest color: %s", shellState.AutosuggestColorString[1:])

	// start
	shellState.Mux()
}

func rgbaToColorString(r, g, b, _ uint32) string {
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r/255, g/255, b/255)
}

// TODO add a diagram of streams here
func (this *ShellState) Mux() {
	parentInBuffer := []byte{}
	childOutBuffer := []byte{}

	for {
		select {
		case <-this.Butterfish.Ctx.Done():
			return

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
			this.PendingAutosuggest = result
			// request cursor position
			this.ParentOut.Write([]byte("\x1b[6n"))

		// We finished with prompt output response, go back to normal mode
		case output := <-this.PromptOutputChan:
			this.History.Add(historyTypeLLMOutput, string(output.Data))
			this.ChildIn.Write([]byte("\n"))
			this.RequestAutosuggest(0, "")

			if len(childOutBuffer) > 0 {
				this.ParentOut.Write(childOutBuffer)
				this.History.Append(historyTypeShellOutput, string(childOutBuffer))
				childOutBuffer = []byte{}
			}

			this.State = stateNormal
			log.Printf("State change: promptResponse -> normal")

		case childOutMsg := <-this.ChildOutReader:
			if childOutMsg == nil {
				log.Println("Child out reader closed")
				this.Butterfish.Cancel()
				return
			}

			// If we're actively printing a response we buffer child output
			if this.State == statePromptResponse {
				childOutBuffer = append(childOutBuffer, childOutMsg.Data...)
				continue
			}

			this.History.Append(historyTypeShellOutput, string(childOutMsg.Data))
			this.ParentOut.Write(childOutMsg.Data)

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
				leftover := this.InputFromParent(this.Butterfish.Ctx, data)

				if leftover == nil || len(leftover) == 0 {
					break
				}
				if len(leftover) == len(data) {
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

	// check if this is a message telling us the cursor position
	_, col, cursorPosLen, ok := parseCursorPos(data)
	if ok {
		// This is wonky and probably needs to be reworked.
		// Finding the cursor position is done by writing \x1b[6n to the terminal
		// (printing on parentOut), and then looking for the response that looks
		// like \x1b[%d;%dR. We request cursor position in 2 cases:
		// 1. When we start a prompt, so that we can wrap the prompt correctly
		// 2. When we get an autosuggest result, so that we can wrap the autosuggest
		// There are almost certainly some race conditions here though, since
		// we request cursor position and then go back to the Mux loop.
		pending := this.PendingAutosuggest
		this.PendingAutosuggest = nil

		if this.State == statePrompting {
			if pending != nil {
				// if we have a pending autosuggest, use it
				this.ShowAutosuggest(this.Prompt, pending, col-1, this.TerminalWidth)
			} else {
				// otherwise we're in a situation where we've just started a prompt
				this.Prompt.SetPromptLength(col - 1 - this.Prompt.Size())
			}
		} else if this.State == stateShell || this.State == stateNormal {
			if pending != nil {
				this.ShowAutosuggest(this.Command, pending, col-1, this.TerminalWidth)
			}
		}
		return data[cursorPosLen:] // don't write the data to the child
	}

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

		// check if the first character is uppercase
		// TODO handle the case where this input is more than a single character, contains other stuff like carriage return, etc
		if unicode.IsUpper(rune(data[0])) {
			this.State = statePrompting
			log.Printf("State change: normal -> prompting")
			this.Prompt.Clear()
			this.Prompt.Write(string(data))

			// Write the actual prompt start
			this.ParentOut.Write([]byte(this.PromptColorString))
			this.ParentOut.Write(data)

			// We're starting a prompt managed here in the wrapper, so we want to
			// get the cursor position
			this.ParentOut.Write([]byte("\x1b[6n"))

		} else if data[0] == '\t' { // user is asking to fill in an autosuggest
			if this.LastAutosuggest != "" {
				this.RealizeAutosuggest(this.Command, true, this.CommandColorString)
				this.State = stateShell
				log.Printf("State change: normal -> shell")
				return data[1:]
			} else {
				// no last autosuggest found, just forward the tab
				this.ChildIn.Write(data)
			}

		} else if data[0] == '\r' {
			this.ChildIn.Write(data)

		} else {
			this.Command = NewShellBuffer()
			this.Command.Write(string(data))

			if this.Command.Size() > 0 {
				this.RefreshAutosuggest(data, this.Command, this.CommandColorString)
				log.Printf("State change: normal -> shell")
				this.State = stateShell
				this.History.NewBlock()
			}

			this.ParentOut.Write([]byte(this.CommandColorString))
			this.ChildIn.Write(data)
		}

	case statePrompting:
		// check if the input contains a newline
		if hasCarriageReturn {
			this.ClearAutosuggest(this.CommandColorString)
			index := bytes.Index(data, []byte{'\r'})
			toAdd := data[:index]
			toPrint := this.Prompt.Write(string(toAdd))

			this.ParentOut.Write(toPrint)
			this.ParentOut.Write([]byte("\n\r"))

			this.SendPrompt()
			return data[index+1:]

		} else if data[0] == '\t' { // user is asking to fill in an autosuggest
			// Tab was pressed, fill in lastAutosuggest
			if this.LastAutosuggest != "" {
				this.RealizeAutosuggest(this.Prompt, false, this.PromptColorString)
			} else {
				// no last autosuggest found, just forward the tab
				this.ParentOut.Write(data)
			}

			return data[1:]

		} else if data[0] == 0x03 { // Ctrl-C
			toPrint := this.Prompt.Clear()
			this.ParentOut.Write(toPrint)
			this.ParentOut.Write([]byte(this.CommandColorString))
			this.State = stateNormal
			log.Printf("State change: prompting -> normal")

		} else { // otherwise user is typing a prompt
			toPrint := this.Prompt.Write(string(data))
			this.ParentOut.Write(toPrint)
			this.RefreshAutosuggest(data, this.Prompt, this.CommandColorString)

			if this.Prompt.Size() == 0 {
				this.ParentOut.Write([]byte(this.CommandColorString)) // reset color
				this.State = stateNormal
				log.Printf("State change: prompting -> normal")
			}
		}

	case stateShell:
		if hasCarriageReturn { // user is submitting a command
			this.ClearAutosuggest(this.CommandColorString)

			this.State = stateNormal
			log.Printf("State change: shell -> normal")

			index := bytes.Index(data, []byte{'\r'})
			this.ChildIn.Write(data[:index+1])
			this.Command = NewShellBuffer()
			this.History.NewBlock()

			return data[index+1:]

		} else if data[0] == '\t' { // user is asking to fill in an autosuggest
			// Tab was pressed, fill in lastAutosuggest
			if this.LastAutosuggest != "" {
				this.RealizeAutosuggest(this.Command, true, this.CommandColorString)
			} else {
				// no last autosuggest found, just forward the tab
				this.ChildIn.Write(data)
			}
			return data[1:]

		} else { // otherwise user is typing a command
			this.Command.Write(string(data))
			this.RefreshAutosuggest(data, this.Command, this.CommandColorString)
			this.ChildIn.Write(data)
			if this.Command.Size() == 0 {
				this.State = stateNormal
				log.Printf("State change: shell -> normal")
			}
		}

	default:
		panic("Unknown state")
	}

	return nil
}

func (this *ShellState) SendPrompt() {
	this.State = statePromptResponse
	log.Printf("State change: prompting -> promptResponse")

	historyBlocks := this.History.GetLastNBytes(this.Butterfish.Config.ShellPromptHistoryWindow)
	requestCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	this.PromptResponseCancel = cancel

	sysMsg, err := this.Butterfish.PromptLibrary.GetPrompt(prompt.PromptShellSystemMessage)
	if err != nil {
		log.Printf("Error getting system message prompt: %s", err)
		this.State = stateNormal
		log.Printf("State change: promptResponse -> normal")
		return
	}

	request := &util.CompletionRequest{
		Ctx:           requestCtx,
		Prompt:        this.Prompt.String(),
		Model:         this.Butterfish.Config.ShellPromptModel,
		MaxTokens:     512,
		Temperature:   0.7,
		HistoryBlocks: historyBlocks,
		SystemMessage: sysMsg,
	}

	this.History.Add(historyTypePrompt, this.Prompt.String())

	// we run this in a goroutine so that we can still receive input
	// like Ctrl-C while waiting for the response
	go func() {
		output, err := this.Butterfish.LLMClient.CompletionStream(request, this.PromptAnswerWriter)
		if err != nil {
			errStr := fmt.Sprintf("Error prompting in shell: %s\n", err)

			// This error means the user needs to set up a subscription, give advice
			if strings.Contains(errStr, ERR_429) {
				errStr = fmt.Sprintf("%s\n%s", errStr, ERR_429_HELP)
			}

			log.Printf("%s", errStr)

			if !strings.Contains(errStr, "context canceled") {
				fmt.Fprintf(this.PromptAnswerWriter, "%s", errStr)
			}
		}

		this.PromptOutputChan <- &byteMsg{Data: []byte(output)}
	}()

	this.Prompt.Clear()
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
		log.Printf("Autosuggest result is old, ignoring")
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
	buf := this.AutosuggestBuffer.WriteAutosuggest(suggToAdd, jumpForward, this.AutosuggestColorString)

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

	historyBlocks := HistoryBlocksToString(this.History.GetLastNBytes(this.Butterfish.Config.ShellAutosuggestHistoryWindow))

	var llmPrompt string
	var err error

	if len(command) == 0 {
		// command completion when we haven't started a command
		llmPrompt, err = this.Butterfish.PromptLibrary.GetPrompt(prompt.PromptShellAutosuggestNewCommand,
			"history", historyBlocks)
	} else if !unicode.IsUpper(rune(command[0])) {
		// command completion when we have started typing a command
		llmPrompt, err = this.Butterfish.PromptLibrary.GetPrompt(prompt.PromptShellAutosuggestCommand,
			"history", historyBlocks,
			"command", command)
	} else {
		// prompt completion, like we're asking a question
		llmPrompt, err = this.Butterfish.PromptLibrary.GetPrompt(prompt.PromptShellAutosuggestPrompt,
			"history", historyBlocks,
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
