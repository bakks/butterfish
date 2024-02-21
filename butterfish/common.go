package butterfish

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"
)

// See https://platform.openai.com/docs/models/overview
var MODEL_TO_NUM_TOKENS = map[string]int{
	"gpt-4":                       8192,
	"gpt-4-1106":                  128000,
	"gpt-4-vision":                128000,
	"gpt-4-0314":                  8192,
	"gpt-4-0613":                  8192,
	"gpt-4-32k":                   32768,
	"gpt-4-32k-0314":              32768,
	"gpt-4-32k-0613":              32768,
	"gpt-3.5-turbo":               4096,
	"gpt-3.5-turbo-0301":          4096,
	"gpt-3.5-turbo-0613":          4096,
	"gpt-3.5-turbo-1106":          16384,
	"gpt-3.5-turbo-16k":           16384,
	"gpt-3.5-turbo-16k-0613":      16384,
	"gpt-3.5-turbo-instruct":      4096,
	"gpt-3.5-turbo-instruct-0913": 4096,
	"text-davinci-003":            2047,
	"text-davinci-002":            2047,
	"code-davinci-002":            8001,
	"code-davinci-001":            8001,
	"text-curie-001":              2049,
	"text-babbage-001":            2049,
	"text-ada-001":                2049,
	"davinci":                     2049,
	"curie":                       2049,
	"babbage":                     2049,
	"ada":                         2049,
	"code-cushman-002":            2048,
	"code-cushman-001":            2048,
}

// these token numbers come from
// https://github.com/pkoukk/tiktoken-go#counting-tokens-for-chat-api-calls
var MODEL_TO_TOKENS_PER_MESSAGE = map[string]int{
	"gpt-4":                  3,
	"gpt-4-0314":             3,
	"gpt-4-0613":             3,
	"gpt-4-32k":              3,
	"gpt-4-32k-0314":         3,
	"gpt-4-32k-0613":         3,
	"gpt-3.5-turbo":          4,
	"gpt-3.5-turbo-0301":     4,
	"gpt-3.5-turbo-1613":     4,
	"gpt-3.5-turbo-1106":     4,
	"gpt-3.5-turbo-16k":      4,
	"gpt-3.5-turbo-16k-0613": 4,
}

// Given a model name (e.g. gpt-4-32k-0613), search the kv map for the
// value associated with the model name. If the model name is not found,
// attempt to find a simpler model name by removing the last segment
// (delimited by -) and searching again.
// returns (model found, value)
func findModelValue(model string, kv map[string]int) (string, int) {
	value, ok := kv[model]
	if ok {
		return model, value
	}

	// attempt to find model by removing the last segment (delimited by -)
	simplerModel := model
	for true {
		lastDash := strings.LastIndex(simplerModel, "-")
		if lastDash == -1 {
			break
		}

		simplerModel = simplerModel[:lastDash]
		value, ok = kv[simplerModel]
		if ok {
			return simplerModel, value
		}
	}

	return "", -1
}

func NumTokensForModel(model string) int {
	foundModel, numTokens := findModelValue(model, MODEL_TO_NUM_TOKENS)

	// couldn't find model
	if foundModel == "" {
		log.Printf("WARNING: Unknown model %s, using default context window size of 2048 tokens", model)
		return 2048
	}

	// found simpler model
	if foundModel != model {
		log.Printf("WARNING: Unknown model %s, using model %s settings instead with context window size of %d tokens", model, foundModel, numTokens)
		return numTokens
	}

	log.Printf("Found model %s context window size of %d tokens", model, numTokens)

	// normal
	return numTokens
}

func NumTokensPerMessageForModel(model string) int {
	foundModel, numTokens := findModelValue(model, MODEL_TO_TOKENS_PER_MESSAGE)

	if foundModel == "" {
		log.Printf("WARNING: Unknown model %s, using default num tokens per message 5", model)
		return 5
	}

	return numTokens
}

// Data type for passing byte chunks from a wrapped command around
type byteMsg struct {
	Data []byte
}

type cursorPosition struct {
	Row    int
	Column int
}

func NewByteMsg(data []byte) *byteMsg {
	buf := make([]byte, len(data))
	copy(buf, data)
	return &byteMsg{
		Data: buf,
	}
}

// Given an io.Reader we write byte chunks to a channel
func readerToChannel(input io.Reader, c chan<- *byteMsg) {
	buf := make([]byte, 1024*16)

	// Loop indefinitely
	for {
		// Read from stream
		n, err := input.Read(buf)

		// Check for error
		if err != nil {
			if err != io.EOF {
				log.Printf("Error reading from file: %s\n", err)
			}
			break
		}

		if n >= 2 && buf[0] == '\x1b' && buf[1] == '[' && !ansiCsiPattern.Match(buf[:n]) {
			log.Printf("Got incomplete escape sequence: %x, this may not be handled correctly and could indicate something weird going on with the child shell", buf)
		}

		c <- NewByteMsg(buf[:n])
	}

	// Close the channel
	close(c)
}

// compile a regex that matches \x1b[%d;%dR
var cursorPosRegex = regexp.MustCompile(`\x1b\[(\d+);(\d+)R`)

// Search for an ANSI cursor position sequence, e.g. \x1b[4;14R, and return:
// - row
// - column
// - length of the sequence
// - whether the sequence was found
func parseCursorPos(data []byte) (int, int, bool) {
	matches := cursorPosRegex.FindSubmatch(data)
	if len(matches) != 3 {
		return -1, -1, false
	}
	row, err := strconv.Atoi(string(matches[1]))
	if err != nil {
		return -1, -1, false
	}
	col, err := strconv.Atoi(string(matches[2]))
	if err != nil {
		return -1, -1, false
	}
	return row, col, true
}

// Given an io.Reader we write byte chunks to a channel
// This is a modified version with a separate channel for cursor position
func readerToChannelWithPosition(input io.Reader, c chan<- *byteMsg, pos chan<- *cursorPosition) {
	buf := make([]byte, 1024*16)

	// Loop indefinitely
	for {
		// Read from stream
		n, err := input.Read(buf)

		// Check for error
		if err != nil {
			if err != io.EOF {
				log.Printf("Error reading from file: %s\n", err)
			}
			break
		}

		// if we find a cursor position, extract it from data and write it to the pos chan
		row, col, found := parseCursorPos(buf[:n])
		if found {
			pos <- &cursorPosition{
				Row:    row,
				Column: col,
			}

			cleaned := cursorPosRegex.ReplaceAll(buf[:n], []byte{})
			copy(buf, cleaned)
			n = len(cleaned)
			if n == 0 {
				continue
			}
		}

		if n >= 2 && buf[0] == '\x1b' && buf[1] == '[' && !ansiCsiPattern.Match(buf[:n]) {
			log.Printf("Got incomplete escape sequence: %x, this may not be handled correctly and could indicate something weird going on with the child shell", buf)
		}

		c <- NewByteMsg(buf[:n])
	}

	// Close the channel
	close(c)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type LoggingBox struct {
	Title    string
	Content  string
	Children []LoggingBox
	Color    int
}

// Box drawing characters with curved corners
const NW_CORNER = "╭"
const NE_CORNER = "╮"
const SW_CORNER = "╰"
const SE_CORNER = "╯"
const H_LINE = "─"
const V_LINE = "│"
const BOX_WIDTH = 80

var BOX_COLORS = []string{
	"\033[38;2;119;221;221m",
	"\033[38;2;253;122;72m",
	"\033[38;2;253;253;150m",
	"\033[38;2;119;221;119m",
	"\033[38;2;174;198;207m",
	"\033[38;2;119;158;203m",
	"\033[38;2;177;156;217m",
	"\033[38;2;255;177;209m",
}

// Given a loggingbox and a writer, write boxes with lines and width 80.
// The boxes can be nested, and the title will be placed in the top line of
// the box.
func PrintLoggingBox(box LoggingBox) {
	// create a writer to a string buffer
	buf := new(bytes.Buffer)
	buf.WriteString("\n")
	printLoggingBox(box, buf, 0, []string{})
	buf.WriteString("\033[0m")
	log.Println(buf.String())
}

// wrap a string based on a rune array, don't worry about spacing or word wrapping
func wrapStringRunes(s string, width int) []string {
	runes := []rune(s)
	lines := []string{}
	line := ""
	for _, r := range runes {
		if len(line)+1 > width {
			// Start a new line
			lines = append(lines, line)
			line = string(r)
		} else {
			// Add to the current line
			if line == "" {
				line = string(r)
			} else {
				line += string(r)
			}
		}
	}
	lines = append(lines, line)
	return lines
}

func printLoggingBox(box LoggingBox, writer io.Writer, depth int, colors []string) {
	//indent := strings.Repeat(V_LINE, depth)
	indent := ""
	for i := 0; i < depth; i++ {
		indent += colors[i] + V_LINE
	}
	boxColor := BOX_COLORS[box.Color]
	indentFull := indent + boxColor + V_LINE
	indent += "\033[0m"
	indentFull += "\033[0m"

	indentRight := ""
	for i := depth - 1; i >= 0; i-- {
		indentRight += colors[i] + V_LINE
	}
	indentRightFull := boxColor + V_LINE + indentRight

	//indentFull := strings.Repeat(V_LINE, depth+1)
	indentLen := depth
	indentFullLen := depth + 1

	title := fmt.Sprintf(" %s ", box.Title)

	// Print the title
	topline := fmt.Sprintf("%s%s%s%s%s%s%s\n",
		indent,
		boxColor,
		NW_CORNER,
		title,
		strings.Repeat(H_LINE, BOX_WIDTH-len(title)-2-indentLen*2),
		NE_CORNER,
		indentRight)

	writer.Write([]byte(topline))

	// Print the content, wrap lines if necessary
	if box.Content != "" {
		content := stripANSI(box.Content)
		// replace tabs with 2 spaces
		content = strings.ReplaceAll(content, "\t", "  ")
		contentLines := strings.Split(content, "\n")
		wrappedContentLines := []string{}
		boxWidth := BOX_WIDTH - 2*(depth+1)

		for _, line := range contentLines {
			wrappedLines := wrapStringRunes(line, boxWidth)
			wrappedContentLines = append(wrappedContentLines, wrappedLines...)
		}

		for _, line := range wrappedContentLines {
			lineWidth := runewidth.StringWidth(line)
			paddingLen := BOX_WIDTH - lineWidth - indentFullLen*2
			padding := ""
			if paddingLen > 0 {
				padding = strings.Repeat(" ", paddingLen)
			}

			line = fmt.Sprintf("%s%s%s%s\n",
				indentFull,
				line,
				padding,
				indentRightFull)
			writer.Write([]byte(line))
		}
	}

	// Print the children
	for _, child := range box.Children {
		printLoggingBox(child, writer, depth+1, append(colors, boxColor))
	}

	// Print the bottom line
	bottomLine := fmt.Sprintf("%s%s%s%s%s%s\n",
		indent,
		boxColor,
		SW_CORNER,
		strings.Repeat(H_LINE, BOX_WIDTH-indentLen*2-2),
		SE_CORNER,
		indentRight)
	writer.Write([]byte(bottomLine))
}

var sysInfo string

func GetSystemInfo() string {
	// cache this
	if sysInfo != "" {
		return sysInfo
	}

	// run uname -a
	cmd := exec.Command("uname", "-a")
	out, err := cmd.Output()
	if err != nil {
		log.Println("Error running uname -a: ", err)
		return ""
	}
	sysInfo = string(out)
	return sysInfo
}
