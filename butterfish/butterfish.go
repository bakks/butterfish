package butterfish

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/creack/pty"
	"github.com/mitchellh/go-homedir"
	"golang.org/x/term"

	"github.com/bakks/butterfish/charmcomponents/console"
	"github.com/bakks/butterfish/embedding"
	"github.com/bakks/butterfish/prompt"
	"github.com/bakks/butterfish/util"
)

// Main driver for the Butterfish set of command line tools. These are tools
// for using AI capabilities on the command line.

type ColorScheme struct {
	Foreground string
	Background string
	Error      string
	Color1     string
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
	Color1:     "#bb8b26", // green
	Color2:     "#fabd2f", // yellow
	Color3:     "#458588", // blue
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

		c <- NewByteMsg(buf[:n])
	}

	// Close the channel
	close(c)
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

// Based on example at https://github.com/creack/pty
// Apparently you can't start a shell like zsh without
// this more complex command execution
func wrapCommand(ctx context.Context, cancel context.CancelFunc, command []string, client *IPCClient) error {
	// Create arbitrary command.
	c := exec.Command(command[0], command[1:]...)

	// Start the command with a pty.
	ptmx, err := pty.Start(c)
	if err != nil {
		return err
	}
	// Make sure to close the pty at the end.
	defer func() { _ = ptmx.Close() }() // Best effort.

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
	ch <- syscall.SIGWINCH                        // Initial resize.
	defer func() { signal.Stop(ch); close(ch) }() // Cleanup signals when done.

	// Set stdin in raw mode.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		panic(err)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }() // Best effort.

	parentIn := make(chan *byteMsg)
	childOut := make(chan *byteMsg)
	remoteIn := make(chan *byteMsg)

	// Read from this process Stdin and write to stdinChannel
	go readerToChannel(os.Stdin, parentIn)
	// Read from pty Stdout and write to stdoutChannel
	go readerToChannel(ptmx, childOut)
	// Read from remote
	go packageRPCStream(client, remoteIn)

	client.SendWrappedCommand(strings.Join(command, " "))

	wrappingMultiplexer(ctx, cancel, client, ptmx, parentIn, remoteIn, childOut)

	return nil
}

const GPTMaxTokens = 1024

func (this *ButterfishCtx) CalculateEmbeddings(ctx context.Context, content []string) ([][]float64, error) {
	return this.gptClient.Embeddings(ctx, content)
}

// A local printf that writes to the butterfishctx out using a lipgloss style
func (this *ButterfishCtx) StylePrintf(style lipgloss.Style, format string, a ...any) {
	str := util.MultilineLipglossRender(style, fmt.Sprintf(format, a...))
	this.out.Write([]byte(str))
}

func (this *ButterfishCtx) Printf(format string, a ...any) {
	this.StylePrintf(this.config.Styles.Foreground, format, a...)
}

func (this *ButterfishCtx) ErrorPrintf(format string, a ...any) {
	this.StylePrintf(this.config.Styles.Error, format, a...)
}

func (this *ButterfishCtx) SummarizeChunks(chunks [][]byte) error {
	writer := util.NewStyledWriter(this.out, this.config.Styles.Foreground)

	if len(chunks) == 1 {
		// the entire document fits within the token limit, summarize directly
		prompt, err := this.promptLibrary.GetPrompt(prompt.PromptSummarize,
			"content", string(chunks[0]))
		if err != nil {
			return err
		}
		return this.gptClient.CompletionStream(this.ctx, prompt, "", writer)
	}

	// the document doesn't fit within the token limit, we'll iterate over it
	// and summarize each chunk as facts, then ask for a summary of facts
	facts := strings.Builder{}

	for _, chunk := range chunks {
		if len(chunk) < 16 { // if we have a tiny chunk, skip it
			break
		}

		prompt, err := this.promptLibrary.GetPrompt(prompt.PromptSummarizeFacts,
			"content", string(chunk))
		if err != nil {
			return err
		}
		resp, err := this.gptClient.Completion(this.ctx, prompt, this.out)
		if err != nil {
			return err
		}
		facts.WriteString(resp)
		facts.WriteString("\n")
	}

	mergedFacts := facts.String()
	prompt, err := this.promptLibrary.GetPrompt(prompt.PromptSummarizeListOfFacts,
		"content", mergedFacts)
	if err != nil {
		return err
	}
	return this.gptClient.CompletionStream(this.ctx, prompt, "", writer)
}

type ButterfishCtx struct {
	ctx              context.Context              // global context, should be passed through to other calls
	cancel           context.CancelFunc           // cancel function for the global context
	promptLibrary    *prompt.PromptLibrary        // library of prompts
	inConsoleMode    bool                         // true if we're running in console mode
	config           *ButterfishConfig            // configuration
	gptClient        *GPT                         // GPT client
	out              io.Writer                    // output writer
	commandRegister  string                       // landing space for generated commands
	consoleCmdChan   <-chan string                // channel for console commands
	clientController ClientController             // client controller
	vectorIndex      embedding.FileEmbeddingIndex // embedding index for searching local files
}

// Ensure we have a vector index object, idempotent
func (this *ButterfishCtx) initVectorIndex(pathsToLoad []string) error {
	if this.vectorIndex != nil {
		return nil
	}

	out := util.NewStyledWriter(this.out, this.config.Styles.Foreground)
	index := embedding.NewDiskCachedEmbeddingIndex(out)
	index.SetEmbedder(this)

	if this.config.Verbose {
		index.SetOutput(this.out)
	}

	this.vectorIndex = index

	if !this.inConsoleMode {
		// if we're running from the command line then we first load the curr
		// dir index
		if pathsToLoad == nil || len(pathsToLoad) == 0 {
			pathsToLoad = []string{"."}
		}

		err := this.vectorIndex.LoadPaths(this.ctx, pathsToLoad)
		if err != nil {
			return err
		}
	}

	return nil
}

func (this *ButterfishCtx) printError(err error, prefix ...string) {
	if len(prefix) > 0 {
		fmt.Fprintf(this.out, "%s error: %s\n", prefix[0], err.Error())
	} else {
		fmt.Fprintf(this.out, "Error: %s\n", err.Error())
	}
}

func (this *ButterfishCtx) checkClientOutputForError(client int, openCmd string, output []byte) {
	// Find the client's last command, i.e. what they entered into the shell
	// It's normal for this to error if the client has not entered anything yet,
	// in that case we just return without action
	lastCmd, err := this.clientController.GetClientLastCommand(client)
	if err != nil {
		return
	}

	// interpolate the prompt to ask if there's an error in the output and
	// call GPT, the response will be streamed (but filter the special token
	// combo "NOOP")
	prompt, err := this.promptLibrary.GetPrompt(prompt.PromptWatchShellOutput,
		"shell_name", openCmd,
		"command", lastCmd,
		"output", string(output))
	if err != nil {
		this.printError(err)
	}

	writer := util.NewStyledWriter(this.out, this.config.Styles.Error)
	err = this.gptClient.CompletionStream(this.ctx, prompt, "", writer)
	if err != nil {
		this.printError(err)
		return
	}
}

type styles struct {
	Question   lipgloss.Style
	Answer     lipgloss.Style
	Summarize  lipgloss.Style
	Highlight  lipgloss.Style
	Prompt     lipgloss.Style
	Error      lipgloss.Style
	Foreground lipgloss.Style
	Grey       lipgloss.Style
}

func ColorSchemeToStyles(colorScheme *ColorScheme) *styles {
	return &styles{
		Question:   lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Color3)),
		Answer:     lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Color1)),
		Highlight:  lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Color2)),
		Summarize:  lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Color2)),
		Prompt:     lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Color4)),
		Error:      lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Error)),
		Foreground: lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Foreground)),
		Grey:       lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Grey)),
	}
}

const expectedEnvPath = ".config/butterfish/butterfish.env"

func envPath() string {
	dirname, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	return filepath.Join(dirname, expectedEnvPath)
}

type ButterfishConfig struct {
	Verbose     bool
	OpenAIToken string
	ColorScheme *ColorScheme
	Styles      *styles

	// File for loading/saving the list of prompts
	PromptLibraryPath string

	// How many bytes should we read at a time when summarizing
	SummarizeChunkSize int
	// How many chunks into the input should we summarize
	SummarizeMaxChunks int
}

func MakeButterfishConfig() *ButterfishConfig {
	colorScheme := &GruvboxDark

	promptPath := "~/.config/butterfish/prompts.yaml"
	promptPath, err := homedir.Expand(promptPath)
	if err != nil {
		log.Fatal(err)
	}

	return &ButterfishConfig{
		Verbose:            false,
		ColorScheme:        colorScheme,
		Styles:             ColorSchemeToStyles(colorScheme),
		PromptLibraryPath:  promptPath,
		SummarizeChunkSize: 3600, // This generally fits into 4096 token limits
		SummarizeMaxChunks: 8,    // Summarize 8 chunks before bailing
	}
}

// Let's initialize our prompts. If we have a prompt library file, we'll load it.
// Either way, we'll then add the default prompts to the library, replacing
// loaded prompts only if OkToReplace is set on them. Then we save the library
// at the same path.
func initializePrompts(config *ButterfishConfig, writer io.Writer) (*prompt.PromptLibrary, error) {
	promptLibrary := prompt.NewPromptLibrary(config.PromptLibraryPath)

	if config.Verbose {
		promptLibrary.Verbose = true
		promptLibrary.VerboseWriter = writer
	}

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
		fmt.Fprintf(writer, "Initialized prompt library at %s\n", config.PromptLibraryPath)
	}

	return promptLibrary, nil
}

func RunConsoleClient(ctx context.Context, args []string) error {
	client, err := runIPCClient(ctx)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)

	return wrapCommand(ctx, cancel, args, client) // this is blocking
}

func RunConsole(ctx context.Context, config *ButterfishConfig) error {
	//initLogging(ctx)
	ctx, cancel := context.WithCancel(ctx)

	// initialize console UI
	consoleCommand := make(chan string)
	cmdCallback := func(cmd string) {
		consoleCommand <- cmd
	}
	exitCallback := func() {
		cancel()
	}
	configCallback := func(model console.ConsoleModel) console.ConsoleModel {
		model.SetStyles(config.Styles.Prompt, config.Styles.Question)
		return model
	}
	cons := console.NewConsoleProgram(configCallback, cmdCallback, exitCallback)

	verboseWriter := util.NewStyledWriter(cons, config.Styles.Grey)
	gpt := NewGPT(config.OpenAIToken, config.Verbose, verboseWriter)

	promptLibrary, err := initializePrompts(config, verboseWriter)
	if err != nil {
		return nil
	}

	clientController := RunIPCServer(ctx, cons)

	butterfishCtx := ButterfishCtx{
		ctx:              ctx,
		cancel:           cancel,
		promptLibrary:    promptLibrary,
		inConsoleMode:    true,
		config:           config,
		gptClient:        gpt,
		out:              cons,
		consoleCmdChan:   consoleCommand,
		clientController: clientController,
	}

	// this is blocking
	butterfishCtx.serverMultiplexer()

	return nil
}

func NewButterfish(ctx context.Context, config *ButterfishConfig) (*ButterfishCtx, error) {
	verboseWriter := util.NewStyledWriter(os.Stdout, config.Styles.Grey)
	gpt := NewGPT(config.OpenAIToken, config.Verbose, verboseWriter)

	promptLibrary, err := initializePrompts(config, verboseWriter)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)

	butterfishCtx := &ButterfishCtx{
		ctx:           ctx,
		cancel:        cancel,
		promptLibrary: promptLibrary,
		inConsoleMode: false,
		config:        config,
		gptClient:     gpt,
		out:           os.Stdout,
	}

	return butterfishCtx, nil
}
