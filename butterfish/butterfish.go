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
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/creack/pty"
	"github.com/mitchellh/go-homedir"
	"golang.org/x/term"

	"github.com/bakks/butterfish/bubbles/console"
	"github.com/bakks/butterfish/embedding"
	"github.com/bakks/butterfish/prompt"
	"github.com/bakks/butterfish/util"
)

// Main driver for the Butterfish set of command line tools. These are tools
// for using AI capabilities on the command line.

type ButterfishConfig struct {
	Verbose           bool
	OpenAIToken       string
	LLMClient         LLM
	ColorScheme       *ColorScheme
	Styles            *styles
	PromptLibraryPath string
	PromptLibrary     PromptLibrary

	GencmdModel          string
	GencmdTemperature    float32
	GencmdMaxTokens      int
	ExeccheckModel       string
	ExeccheckTemperature float32
	ExeccheckMaxTokens   int
	SummarizeModel       string
	SummarizeTemperature float32
	SummarizeMaxTokens   int
}

type PromptLibrary interface {
	GetPrompt(name string, args ...string) (string, error)
}

type LLM interface {
	CompletionStream(request *util.CompletionRequest, writer io.Writer) (string, error)
	Completion(request *util.CompletionRequest) (string, error)
	Embeddings(ctx context.Context, input []string) ([][]float64, error)
	Edits(ctx context.Context, content, instruction, model string, temperature float32) (string, error)
}

type ButterfishCtx struct {
	Ctx    context.Context    // global context, should be passed through to other calls
	Cancel context.CancelFunc // cancel function for the global context
	Out    io.Writer          // output writer

	Config          *ButterfishConfig            // configuration
	InConsoleMode   bool                         // true if we're running in console mode
	PromptLibrary   PromptLibrary                // library of prompts
	LLMClient       LLM                          // GPT client
	CommandRegister string                       // landing space for generated commands
	VectorIndex     embedding.FileEmbeddingIndex // embedding index for searching local files

	ConsoleCmdChan   <-chan string    // channel for console commands
	ClientController ClientController // client controller
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

func ptyCommand(ctx context.Context, command []string) (*os.File, func() error, error) {
	// Create arbitrary command.
	var cmd *exec.Cmd
	if len(command) > 1 {
		cmd = exec.CommandContext(ctx, command[0], command[1:]...)
	} else {
		cmd = exec.CommandContext(ctx, command[0])
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

// Based on example at https://github.com/creack/pty
// Apparently you can't start a shell like zsh without
// this more complex command execution
func wrapCommand(ctx context.Context, cancel context.CancelFunc, command []string, client *IPCClient) error {
	ptmx, cleanup, err := ptyCommand(ctx, command)
	if err != nil {
		return err
	}
	defer cleanup()

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
		Question:   lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Color4)),
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

func RunShell(ctx context.Context, config *ButterfishConfig, shell string) error {
	ptmx, ptyCleanup, err := ptyCommand(ctx, []string{shell})
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

// ShellHistory keeps a record of past shell history and LLM interaction in
// a slice of util.HistoryBlock objects. You can add a new block, append to
// the last block, and get the the last n bytes of the history as an array of
// HistoryBlocks.
type ShellHistory struct {
	Blocks []*util.HistoryBlock
}

func NewShellHistory() *ShellHistory {
	return &ShellHistory{
		Blocks: make([]*util.HistoryBlock, 0),
	}
}

func (this *ShellHistory) Add(historyType int, block string) {
	this.Blocks = append(this.Blocks, &util.HistoryBlock{
		Type:    historyType,
		Content: block,
	})
}

func (this *ShellHistory) IdempotentAdd(historyType int, block string) {
	if len(this.Blocks) > 0 && this.Blocks[len(this.Blocks)-1].Type == historyType {
		this.Blocks[len(this.Blocks)-1].Content += block
	} else {
		this.Blocks = append(this.Blocks, &util.HistoryBlock{
			Type:    historyType,
			Content: block,
		})
	}
}

func (this *ShellHistory) AddToLast(content string) {
	if len(this.Blocks) == 0 {
		return
	}
	this.Blocks[len(this.Blocks)-1].Content += content
}

// Go back in history for a certain number of bytes.
// This truncates each block content to a maximum of 2048 bytes.
func (this *ShellHistory) GetLastNBytes(numBytes int) []util.HistoryBlock {
	var blocks []util.HistoryBlock

	for i := len(this.Blocks) - 1; i >= 0; i-- {
		block := this.Blocks[i]
		if numBytes > 0 {
			if len(block.Content) > 2048 {
				block.Content = block.Content[:2048]
			}
			if len(block.Content) > numBytes {
				block.Content = block.Content[:numBytes]
			}
			blocks = append(blocks, *block)
			numBytes -= len(block.Content)
		}
	}

	// reverse the blocks slice
	for i := len(blocks)/2 - 1; i >= 0; i-- {
		opp := len(blocks) - 1 - i
		blocks[i], blocks[opp] = blocks[opp], blocks[i]
	}

	return blocks
}

// TODO add a diagram of streams here
// States:
// 1. Normal
// 2. Prompting

const (
	stateNormal = iota
	stateShell
	statePrompting
)

func (this *ButterfishCtx) ShellMultiplexer(
	childIn io.Writer, childOut io.Reader,
	parentIn io.Reader, parentOut io.Writer) {
	childOutReader := make(chan *byteMsg)
	parentInReader := make(chan *byteMsg)

	go readerToChannel(childOut, childOutReader)
	go readerToChannel(parentIn, parentInReader)

	history := NewShellHistory()
	promptOutputWriter := util.NewStyledWriter(parentOut, this.Config.Styles.Answer)
	cleanedWriter := util.NewReplaceWriter(promptOutputWriter, "\n", "\r\n")

	currState := stateNormal
	prompt := ""
	log.Printf("Starting shell multiplexer")

	for {
		select {
		case childOutMsg := <-childOutReader:
			if childOutMsg == nil {
				return
			}
			parentOut.Write(childOutMsg.Data)
			cleanData := sanitizeTTYData(childOutMsg.Data)
			history.IdempotentAdd(historyTypeShellOutput, string(cleanData))

		case parentInMsg := <-parentInReader:
			if parentInMsg == nil {
				return
			}
			data := parentInMsg.Data
			hasCarriageReturn := bytes.Contains(data, []byte{'\r'})

			switch currState {
			case stateNormal:
				// check if the first character is uppercase
				// TODO handle the case where this input is more than a single character, contains other stuff like carriage return, etc
				if unicode.IsUpper(rune(data[0])) {
					currState = statePrompting
					log.Printf("State change: normal -> prompting")
					prompt = string(data)
					parentOut.Write([]byte(data))
				} else if hasCarriageReturn {
					childIn.Write(data)
				} else {
					log.Printf("State change: normal -> shell")
					currState = stateShell
					childIn.Write(data)
					history.IdempotentAdd(historyTypeShellInput, string(data))
				}

			case statePrompting:
				// check if the input contains a newline
				toAdd := data
				if hasCarriageReturn {
					toAdd = data[:bytes.Index(data, []byte{'\r'})]
					currState = stateNormal
					log.Printf("State change: prompting -> normal")
				}
				prompt += string(toAdd)
				parentOut.Write(toAdd)

				if currState == stateNormal {
					//parentOut.Write([]byte("\n\rPrompting: " + prompt + "\n\r"))
					log.Printf("Prompting: %s", prompt)
					parentOut.Write([]byte("\n\r"))

					historyBlocks := history.GetLastNBytes(2500)
					request := &util.CompletionRequest{
						Ctx:           this.Ctx,
						Prompt:        prompt,
						Model:         "gpt-3.5-turbo",
						MaxTokens:     512,
						Temperature:   0.7,
						HistoryBlocks: historyBlocks,
					}

					//dump, _ := json.Marshal(historyBlocks)
					//fmt.Fprintf(parentOut, "History: %s\n\r", dump)
					output, err := this.LLMClient.CompletionStream(request, cleanedWriter)
					if err != nil {
						panic(err)
					}

					history.Add(historyTypePrompt, prompt)
					history.Add(historyTypeLLMOutput, output)

					childIn.Write([]byte("\n"))
					prompt = ""
				}

			case stateShell:
				//history.IdempotentAdd(historyTypeShellInput, string(data))
				childIn.Write(data)

				if hasCarriageReturn {
					currState = stateNormal
					log.Printf("State change: shell -> normal")
				}

			default:
				panic("Unknown state")
			}

		case <-this.Ctx.Done():
			return
		}
	}

	panic("unreachable")
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

	llmClient, err := initLLM(config)
	if err != nil {
		return err
	}

	clientController := RunIPCServer(ctx, cons)

	promptLibrary, err := initPromptLibrary(config)
	if err != nil {
		return err
	}

	butterfishCtx := ButterfishCtx{
		Ctx:              ctx,
		Cancel:           cancel,
		PromptLibrary:    promptLibrary,
		InConsoleMode:    true,
		Config:           config,
		LLMClient:        llmClient,
		Out:              cons,
		ConsoleCmdChan:   consoleCommand,
		ClientController: clientController,
	}

	// this is blocking
	butterfishCtx.serverMultiplexer()

	return nil
}

func initLLM(config *ButterfishConfig) (LLM, error) {
	if config.OpenAIToken == "" && config.LLMClient != nil {
		return nil, errors.New("Must provide either an OpenAI Token or an LLM client.")
	} else if config.OpenAIToken != "" && config.LLMClient != nil {
		return nil, errors.New("Must provide either an OpenAI Token or an LLM client, not both.")
	} else if config.OpenAIToken != "" {
		verboseWriter := util.NewStyledWriter(os.Stdout, config.Styles.Grey)
		return NewGPT(config.OpenAIToken, config.Verbose, verboseWriter), nil
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
