package butterfish

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

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
	oldLength          int
	newLength          int
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

	this.oldLength = len(this.buffer)

	for i := 0; i < len(runes); i++ {

		if len(runes) >= i+3 && runes[i] == 0x1b && runes[i+1] == 0x5b {
			// we have an escape sequence

			switch runes[i+2] {
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

	this.newLength = len(this.buffer)

	//log.Printf("Buffer update, cursor: %d, buffer: %s, written: %s  %x", this.cursor, string(this.buffer), data, []byte(data))

	return this.calculateShellUpdate(startingCursor)
}

func (this *ShellBuffer) calculateShellUpdate(startingCursor int) []byte {
	// We've updated the buffer. Now we need to figure out what to print.
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
		fmt.Fprintf(w, ESC_LEFT, startingCursor)
		// print the buffer
		fmt.Fprintf(w, "%s", string(this.buffer))
		// go back to the ending cursor
		fmt.Fprintf(w, ESC_LEFT, len(this.buffer)-this.cursor)

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
		fmt.Fprintf(w, ESC_CLEAR)
		fmt.Fprintf(w, ESC_UP, oldCursorLine)
	}
	// go right for the prompt length
	if this.promptLength > 0 {
		fmt.Fprintf(w, ESC_RIGHT, this.promptLength)
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

	// if we deleted text we clear out the rest of the line
	// this may need to be more sophisticated
	if this.newLength < this.oldLength {
		w.Write([]byte(ESC_CLEAR))
	}

	// if the cursor is not at the end of the buffer we need to adjust it because
	// we rewrote the entire buffer
	if this.cursor < len(this.buffer) {
		// carriage return to go to left side of term
		w.Write([]byte{'\r'})
		// go up for the number of lines
		if newNumLines-newCursorLine > 0 {
			fmt.Fprintf(w, ESC_UP, newNumLines-newCursorLine)
		}
		// go right to the new cursor column
		if newColumn > 0 {
			fmt.Fprintf(w, ESC_RIGHT, newColumn)
		}
	}

	return buf.Bytes()
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
// :          ^ cursor
// autosuggest: " foobar"
// jumpForward: 0
//
// >>> command
// :        ^ cursor
// autosuggest: " foobar"
// jumpForward: 2
func (this *ShellBuffer) WriteAutosuggest(autosuggestText string, jumpForward int, colorStr string) []byte {
	// promptlength represents the starting cursor position in this context

	var w io.Writer
	// create writer to a string buffer
	var buf bytes.Buffer
	w = &buf

	// maybe -1
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
		fmt.Fprintf(w, ESC_RIGHT, jumpForward)
	}

	// handle color
	if colorStr != "" {
		w.Write([]byte(colorStr))
	} else if this.color != "" {
		w.Write([]byte(this.color))
	}

	// write the autosuggest text
	w.Write([]byte(autosuggestText))
	w.Write([]byte(CLEAR_COLOR))

	// return cursor to original position

	// carriage return to go to left side of term
	w.Write([]byte{'\r'})

	// go up for the number of lines
	if numLines > 0 {
		fmt.Fprintf(w, ESC_UP, numLines)
	}

	// go right for the prompt length
	if this.promptLength > 0 {
		fmt.Fprintf(w, ESC_RIGHT, this.promptLength)
	}

	return buf.Bytes()
}

func (this *ShellBuffer) ClearLast(colorStr string) []byte {
	//log.Printf("Clearing last autosuggest, lastAutosuggestLen: %d, lastJumpForward: %d, promptLength: %d", this.lastAutosuggestLen, this.lastJumpForward, this.promptLength)
	emptyBuf := strings.Repeat(" ", this.lastAutosuggestLen)
	return this.WriteAutosuggest(emptyBuf, this.lastJumpForward, colorStr)
}

func (this *ShellBuffer) EatAutosuggestRune() {
	if this.lastJumpForward > 0 {
		panic("jump forward should be 0")
	}

	this.lastAutosuggestLen--
	this.promptLength++
}
