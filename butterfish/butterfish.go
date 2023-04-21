package butterfish

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/creack/pty"
	"github.com/mitchellh/go-homedir"
	"golang.org/x/term"

	"github.com/bakks/butterfish/embedding"
	"github.com/bakks/butterfish/prompt"
	"github.com/bakks/butterfish/util"
)

// Main driver for the Butterfish set of command line tools. These are tools
// for using AI capabilities on the command line.

// Shell to-do
// - Add fallback when you need to start a command with a capital letter
// - Check if the cursor has moved back before doing autocomplete

type ButterfishConfig struct {
	// Verbose mode, prints out more information like raw OpenAI communication
	Verbose bool

	// OpenAI private token, should start with "sk-".
	// Found at https://platform.openai.com/account/api-keys
	OpenAIToken string

	// LLM API communication client that implements the LLM interface
	LLMClient LLM

	// Color scheme to use for the shell, see GruvboxDark below
	ColorScheme *ColorScheme

	// A list of context-specific styles drawn from the colorscheme
	// These are what should actually be used during rendering
	Styles *styles

	// Path of yaml file from which to load LLM prompts
	// Defaults to ~/.config/butterfish/prompts.yaml
	PromptLibraryPath string

	// The instantiated prompt library used when interpolating prompts before
	// calling the LLM
	PromptLibrary PromptLibrary

	// Shell mode configuration
	ShellMode                bool
	ShellPromptModel         string // used when the user enters an explicit prompt
	ShellPromptHistoryWindow int    // how many bytes of history to include in the prompt
	ShellCommandPrompt       string // replace the default command prompt (eg >) with this
	ShellAutosuggestEnabled  bool   // whether to use autosuggest
	ShellAutosuggestModel    string // used when we're autocompleting a command
	// how long to wait between when the user stos typing and we ask for an
	// autosuggest
	ShellAutosuggestTimeout       time.Duration
	ShellAutosuggestHistoryWindow int // how many bytes of history to include when autosuggesting

	// Model, temp, and max tokens to use when executing the `gencmd` command
	GencmdModel       string
	GencmdTemperature float32
	GencmdMaxTokens   int

	// Model, temp, and max tokens to use when executing the `exec` command
	ExeccheckModel       string
	ExeccheckTemperature float32
	ExeccheckMaxTokens   int

	// Model, temp, and max tokens to use when executing the `summarize` command
	SummarizeModel       string
	SummarizeTemperature float32
	SummarizeMaxTokens   int
}

// Interface for a library that accepts a prompt and interpolates variables
// within the prompt
type PromptLibrary interface {
	// Get a prompt by name. The arguments are passed in a pattern of key, value.
	// For example, if the prompt is "Hello, {name}", then you would call
	// GetPrompt("greeting", "name", "Peter") and "Hello, Peter" would be
	// returned. If a variable is not found, or an argument is passed that doesn't
	// have a corresponding variable, an error is returned.
	GetPrompt(name string, args ...string) (string, error)
}

// A generic interface for a service that calls a large larguage model based
// on input prompts.
type LLM interface {
	CompletionStream(request *util.CompletionRequest, writer io.Writer) (string, error)
	Completion(request *util.CompletionRequest) (string, error)
	Embeddings(ctx context.Context, input []string) ([][]float64, error)
	Edits(ctx context.Context, content, instruction, model string, temperature float32) (string, error)
}

type ButterfishCtx struct {
	// global context, should be passed through to other calls
	Ctx context.Context
	// cancel function for the global context
	Cancel context.CancelFunc
	// output writer
	Out io.Writer

	// configuration
	Config *ButterfishConfig
	// true if we're running in console mode
	InConsoleMode bool
	// library of prompts
	PromptLibrary PromptLibrary
	// GPT client
	LLMClient LLM
	// landing space for generated commands
	CommandRegister string
	// embedding index for searching local files
	VectorIndex embedding.FileEmbeddingIndex
}

type ColorScheme struct {
	Foreground string // neutral foreground color
	Background string
	Error      string // should be reddish
	Color1     string // should be greenish
	Color2     string
	Color3     string
	Color4     string
	Color5     string
	Color6     string
	Grey       string
}

// Gruvbox Colorscheme
// from https://github.com/morhetz/gruvbox
var GruvboxDark = ColorScheme{
	Foreground: "#ebdbb2",
	Background: "#282828",
	Error:      "#fb4934", // red
	Color1:     "#b8bb26", // green
	Color2:     "#fabd2f", // yellow
	Color3:     "#83a598", // blue
	Color4:     "#d3869b", // magenta
	Color5:     "#8ec07c", // cyan
	Color6:     "#fe8019", // orange
	Grey:       "#928374", // gray
}

var GruvboxLight = ColorScheme{
	Foreground: "#7C6F64",
	Background: "#FBF1C7",
	Error:      "#CC241D",
	Color1:     "#98971A",
	Color2:     "#D79921",
	Color3:     "#458588",
	Color4:     "#B16286",
	Color5:     "#689D6A",
	Color6:     "#D65D0E",
	Grey:       "#928374",
}

const BestCompletionModel = "gpt-3.5-turbo"
const BestAutosuggestModel = "text-davinci-003"

func MakeButterfishConfig() *ButterfishConfig {
	colorScheme := &GruvboxDark

	return &ButterfishConfig{
		Verbose:              false,
		ColorScheme:          colorScheme,
		Styles:               ColorSchemeToStyles(colorScheme),
		GencmdModel:          BestCompletionModel,
		GencmdTemperature:    0.6,
		GencmdMaxTokens:      512,
		ExeccheckModel:       BestCompletionModel,
		ExeccheckTemperature: 0.6,
		ExeccheckMaxTokens:   512,
		SummarizeModel:       BestCompletionModel,
		SummarizeTemperature: 0.7,
		SummarizeMaxTokens:   1024,
	}
}

// Data type for passing byte chunks from a wrapped command around
type byteMsg struct {
	Data []byte
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
			log.Printf("got escape sequence: %x", buf)
			panic("Got incomplete escape sequence")
		}

		c <- NewByteMsg(buf[:n])
	}

	// Close the channel
	close(c)
}

// For Control Sequence Introducer, or CSI, commands, the ESC [ (written as \e[ or \033[ in several programming and scripting languages) is followed by any number (including none) of "parameter bytes" in the range 0x30–0x3F (ASCII 0–9:;<=>?), then by any number of "intermediate bytes" in the range 0x20–0x2F (ASCII space and !"#$%&'()*+,-./), then finally by a single "final byte" in the range 0x40–0x7E (ASCII @A–Z[\]^_`a–z{|}~)
var ansiCsiPattern = regexp.MustCompile("\x1b\\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]")

func incompleteAnsiSequence(buf []byte) bool {
	return bytes.Index(buf, []byte{0x1b, 0x5b}) != -1 && !ansiCsiPattern.Match(buf)
}

// from https://github.com/acarl005/stripansi/blob/master/stripansi.go
const ansiPattern = "[\u001B\u009B][[\\]()#;?]*(?:(?:(?:[a-zA-Z\\d]*(?:;[a-zA-Z\\d]*)*)?\u0007)|(?:(?:\\d{1,4}(?:;\\d{0,4})*)?[\\dA-PRZcf-ntqry=><~]))"

var ansiRegexp = regexp.MustCompile(ansiPattern)

// Strip ANSI tty control codes out of a string
func stripANSI(str string) string {
	return ansiRegexp.ReplaceAllString(str, "")
}

// Function for filtering out non-printable characters from a string
func filterNonPrintable(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\t': // we don't want to filter these out though
			return r

		default:
			if unicode.IsPrint(r) {
				return r
			}
			return -1
		}
	}, s)
}

func sanitizeTTYData(data []byte) []byte {
	return []byte(filterNonPrintable(stripANSI(string(data))))
}

func sanitizeTTYString(data string) string {
	return filterNonPrintable(stripANSI(data))
}

func ptyCommand(ctx context.Context, envVars []string, command []string) (*os.File, func() error, error) {
	// Create arbitrary command.
	var cmd *exec.Cmd

	if len(command) > 1 {
		cmd = exec.CommandContext(ctx, command[0], command[1:]...)
	} else {
		cmd = exec.CommandContext(ctx, command[0])
	}

	cmd.Env = os.Environ()
	if len(envVars) > 0 {
		cmd.Env = append(cmd.Env, envVars...)
	}

	// Start the command with a pty.
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, nil, err
	}

	// Handle pty size.
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			if err := pty.InheritSize(os.Stdin, ptmx); err != nil {
				log.Printf("error resizing pty: %s", err)
			}
		}
	}()
	ch <- syscall.SIGWINCH // Initial resize.

	// Set stdin in raw mode.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		ptmx.Close()
		signal.Stop(ch)
		close(ch)
		return nil, nil, err
	}

	cleanup := func() error {
		err := ptmx.Close()
		if err != nil {
			return err
		}

		signal.Stop(ch)
		close(ch)

		return term.Restore(int(os.Stdin.Fd()), oldState)
	}

	return ptmx, cleanup, nil
}

func (this *ButterfishCtx) CalculateEmbeddings(ctx context.Context, content []string) ([][]float64, error) {
	return this.LLMClient.Embeddings(ctx, content)
}

// A local printf that writes to the butterfishctx out using a lipgloss style
func (this *ButterfishCtx) StylePrintf(style lipgloss.Style, format string, a ...any) {
	str := util.MultilineLipglossRender(style, fmt.Sprintf(format, a...))
	this.Out.Write([]byte(str))
}

func (this *ButterfishCtx) StyleSprintf(style lipgloss.Style, format string, a ...any) string {
	return util.MultilineLipglossRender(style, fmt.Sprintf(format, a...))
}

func (this *ButterfishCtx) Printf(format string, a ...any) {
	this.StylePrintf(this.Config.Styles.Foreground, format, a...)
}

func (this *ButterfishCtx) ErrorPrintf(format string, a ...any) {
	this.StylePrintf(this.Config.Styles.Error, format, a...)
}

// Ensure we have a vector index object, idempotent
func (this *ButterfishCtx) initVectorIndex(pathsToLoad []string) error {
	if this.VectorIndex != nil {
		return nil
	}

	out := util.NewStyledWriter(this.Out, this.Config.Styles.Foreground)
	index := embedding.NewDiskCachedEmbeddingIndex(this, out)

	if this.Config.Verbose {
		index.SetOutput(this.Out)
	}

	this.VectorIndex = index

	if !this.InConsoleMode {
		// if we're running from the command line then we first load the curr
		// dir index
		if pathsToLoad == nil || len(pathsToLoad) == 0 {
			pathsToLoad = []string{"."}
		}

		err := this.VectorIndex.LoadPaths(this.Ctx, pathsToLoad)
		if err != nil {
			return err
		}
	}

	return nil
}

func (this *ButterfishCtx) printError(err error, prefix ...string) {
	if len(prefix) > 0 {
		fmt.Fprintf(this.Out, "%s error: %s\n", prefix[0], err.Error())
	} else {
		fmt.Fprintf(this.Out, "Error: %s\n", err.Error())
	}
}

type styles struct {
	Question   lipgloss.Style
	Answer     lipgloss.Style
	Go         lipgloss.Style
	Summarize  lipgloss.Style
	Highlight  lipgloss.Style
	Prompt     lipgloss.Style
	Error      lipgloss.Style
	Foreground lipgloss.Style
	Grey       lipgloss.Style
}

func (this *styles) PrintTestColors() {
	fmt.Println(this.Question.Render("Question"))
	fmt.Println(this.Answer.Render("Answer"))
	fmt.Println(this.Go.Render("Go"))
	fmt.Println(this.Summarize.Render("Summarize"))
	fmt.Println(this.Highlight.Render("Highlight"))
	fmt.Println(this.Prompt.Render("Prompt"))
	fmt.Println(this.Error.Render("Error"))
	fmt.Println(this.Foreground.Render("Foreground"))
	fmt.Println(this.Grey.Render("Grey"))
}

func ColorSchemeToStyles(colorScheme *ColorScheme) *styles {
	return &styles{
		Question:   lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Color5)),
		Answer:     lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Color2)),
		Go:         lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Color5)),
		Highlight:  lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Color2)),
		Summarize:  lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Color2)),
		Prompt:     lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Color4)),
		Error:      lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Error)),
		Foreground: lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Foreground)),
		Grey:       lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Grey)),
	}
}

// Let's initialize our prompts. If we have a prompt library file, we'll load it.
// Either way, we'll then add the default prompts to the library, replacing
// loaded prompts only if OkToReplace is set on them. Then we save the library
// at the same path.
func NewDiskPromptLibrary(path string, verbose bool, writer io.Writer) (*prompt.DiskPromptLibrary, error) {
	promptLibrary := prompt.NewPromptLibrary(path, verbose, writer)
	loaded := false

	if promptLibrary.LibraryFileExists() {
		err := promptLibrary.Load()
		if err != nil {
			return nil, err
		}
		loaded = true
	}
	promptLibrary.ReplacePrompts(prompt.DefaultPrompts)
	promptLibrary.Save()

	if !loaded {
		fmt.Fprintf(writer, "Wrote prompt library at %s\n", path)
	}

	return promptLibrary, nil
}

func initLLM(config *ButterfishConfig) (LLM, error) {
	if config.OpenAIToken == "" && config.LLMClient != nil {
		return nil, errors.New("Must provide either an OpenAI Token or an LLM client.")
	} else if config.OpenAIToken != "" && config.LLMClient != nil {
		return nil, errors.New("Must provide either an OpenAI Token or an LLM client, not both.")
	} else if config.OpenAIToken != "" {
		verboseWriter := util.NewStyledWriter(os.Stdout, config.Styles.Grey)
		gpt := NewGPT(config.OpenAIToken, config.Verbose, verboseWriter)
		if config.ShellMode && config.Verbose {
			gpt.SetVerboseLogging()
		}
		return gpt, nil
	} else {
		return config.LLMClient, nil
	}
}

func initPromptLibrary(config *ButterfishConfig) (PromptLibrary, error) {
	verboseWriter := util.NewStyledWriter(os.Stdout, config.Styles.Grey)

	if config.PromptLibrary != nil {
		return config.PromptLibrary, nil
	}

	promptPath, err := homedir.Expand(config.PromptLibraryPath)
	if err != nil {
		return nil, err
	}

	return NewDiskPromptLibrary(promptPath, config.Verbose, verboseWriter)
}

func NewButterfish(ctx context.Context, config *ButterfishConfig) (*ButterfishCtx, error) {
	llmClient, err := initLLM(config)
	if err != nil {
		return nil, err
	}

	promptLibrary, err := initPromptLibrary(config)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)

	butterfishCtx := &ButterfishCtx{
		Ctx:           ctx,
		Cancel:        cancel,
		PromptLibrary: promptLibrary,
		InConsoleMode: false,
		Config:        config,
		LLMClient:     llmClient,
		Out:           os.Stdout,
	}

	return butterfishCtx, nil
}
