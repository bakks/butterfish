package main

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
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/lipgloss"
	"github.com/creack/pty"
	"github.com/joho/godotenv"
	"github.com/mitchellh/go-homedir"
	"github.com/spf13/afero"
	"golang.org/x/term"

	"github.com/bakks/butterfish/go/charmcomponents/console"
	"github.com/bakks/butterfish/go/embedding"
	"github.com/bakks/butterfish/go/prompt"
	"github.com/bakks/butterfish/go/util"
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
var gruvboxDark = ColorScheme{
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

var gruvboxLight = ColorScheme{
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

// We're multiplexing here between the stdin/stdout of this
// process, stdin/stdout of the wrapped process, and
// input/output of the connection to the butterfish server.
func wrappingMultiplexer(
	ctx context.Context,
	cancel context.CancelFunc,
	remoteClient *IPCClient,
	childIn io.Writer,
	parentIn, remoteIn, childOut <-chan *byteMsg) {

	buf := bytes.NewBuffer(nil)

	for {
		select {

		// Receive data from this process's stdin and write it to the child
		// process' stdin, add it to a local buffer, send to remote when
		// we hit a carriage return
		case s1 := <-parentIn:
			if bytes.Contains(s1.Data, []byte{'\r'}) {
				// the CR might be in the middle of a longer byte array, so we concat
				// with the existing butter and add any others to the cleared buffer
				concatted := append(buf.Bytes(), s1.Data...)
				split := bytes.Split(concatted, []byte{'\r'})

				if len(split) > 0 && len(split[0]) > 0 {
					buf.Reset()
					if len(split) > 1 {
						buf.Write(split[1])
					}
					// Send to remote
					remoteClient.SendInput(sanitizeTTYData(concatted))
				}
			} else {
				buf.Write(s1.Data)
			}

			_, err := childIn.Write(s1.Data)
			if err != nil {
				log.Printf("Error writing to child process: %s\n", err)
			}

		// Receive data from the child process stdout and write it
		// to this process's stdout and the remote server
		case s2 := <-childOut:
			if s2 == nil {
				return // child process is done, let's bail out
			}

			// Write to this process's stdout
			os.Stdout.Write(s2.Data)

			// Filter out characters and send to server
			printed := sanitizeTTYData(s2.Data)
			err := remoteClient.SendOutput(printed)
			if err != nil {
				if err == io.EOF {
					log.Printf("Remote server closed connection, exiting...\n")
					cancel()
					return
				} else {
					log.Fatalf("Error sending to remote server: %s\n", err)
				}
			}

		// Receive data from the remote server and write it to the
		// child process' stdin
		case s3 := <-remoteIn:
			if s3 == nil {
				cancel()
				return
			}

			_, err := fmt.Fprint(childIn, string(s3.Data))
			if err != nil {
				log.Printf("Error writing to child process: %s\n", err)
			}

		case <-ctx.Done():
			break
		}
	}
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

// Given a file path we attempt to semantically summarize its content.
// If the file is short enough, we ask directly for a summary, otherwise
// we ask for a list of facts and then summarize those.

// From OpenAI documentation:
// Tokens can be words or just chunks of characters. For example, the word
// “hamburger” gets broken up into the tokens “ham”, “bur” and “ger”, while a
// short and common word like “pear” is a single token. Many tokens start with
// a whitespace, for example “ hello” and “ bye”.
// The number of tokens processed in a given API request depends on the length
// of both your inputs and outputs. As a rough rule of thumb, 1 token is
// approximately 4 characters or 0.75 words for English text.
func (this *ButterfishCtx) SummarizePath(path string) error {
	bytesPerChunk := this.config.SummarizeChunkSize
	maxChunks := this.config.SummarizeMaxChunks

	this.StylePrintf(this.config.Styles.Question, "Summarizing %s\n", path)

	fs := afero.NewOsFs()
	chunks, err := util.GetFileChunks(this.ctx, fs, path, uint64(bytesPerChunk), maxChunks)
	if err != nil {
		return err
	}

	return this.SummarizeChunks(chunks)
}

// Iterate through a list of file paths and summarize each
func (this *ButterfishCtx) SummarizePaths(paths []string) error {
	for _, path := range paths {
		err := this.SummarizePath(path)
		if err != nil {
			return err
		}
	}

	return nil
}

// Execute the command stored in commandRegister on the remote host,
// either from the command register or from a command string
func (this *ButterfishCtx) execremoteCommand(cmd string) error {
	if cmd == "" && this.commandRegister == "" {
		return errors.New("No command to execute")
	}
	if cmd == "" {
		cmd = this.commandRegister
	}
	cmd += "\n"

	fmt.Fprintf(this.out, "Executing: %s\n", cmd)
	client := this.clientController.GetClientWithOpenCmdLike("sh")
	if client == -1 {
		return errors.New("No wrapped clients with open command like 'sh' found")
	}

	return this.clientController.Write(client, cmd)
}

func (this *ButterfishCtx) updateCommandRegister(cmd string) {
	// If we're not in console mode then we don't care about updating the register
	if !this.inConsoleMode {
		return
	}

	cmd = strings.TrimSpace(cmd)
	this.commandRegister = cmd
	this.Printf("Command register updated to:\n")
	this.StylePrintf(this.config.Styles.Answer, "%s\n", cmd)
	this.Printf("Run exec or execremote to execute\n")
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

func (this *ButterfishCtx) serverMultiplexer() {
	clientInput := this.clientController.GetReader()
	fmt.Fprintln(this.out, "Butterfish server active...")

	for {
		select {
		case cmd := <-this.consoleCmdChan:
			err := this.Command(cmd)
			if err != nil {
				this.printError(err)
			}

		case clientData := <-clientInput:
			// Find the client's open/wrapping command, e.g. "zsh"
			client := clientData.Client
			wrappedCommand, err := this.clientController.GetClientOpenCommand(client)
			if err != nil {
				this.printError(err, "on client input")
				continue
			}

			// If we think the client is wrapping a shell and the clientdata is
			// greater than 35 bytes (a pretty dumb predicate), we'll check
			// for unexpected / error output and try to be helpful
			if strings.Contains(wrappedCommand, "sh") && len(clientData.Data) > 35 {
				this.checkClientOutputForError(client, wrappedCommand, clientData.Data)
			}

		case <-this.ctx.Done():
			return
		}
	}
}
func initLogging(ctx context.Context) {
	f, err := os.OpenFile("butterfish.log", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
	}
	log.SetOutput(f)

	go func() {
		<-ctx.Done()
		f.Close()
	}()
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

func colorSchemeToStyles(colorScheme *ColorScheme) *styles {
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

func getOpenAIToken() string {
	path := envPath()

	// We attempt to get a token from env vars plus an env file
	godotenv.Load(path)
	token := os.Getenv("OPENAI_TOKEN")

	if token != "" {
		return token
	}

	// If we don't have a token, we'll prompt the user to create one
	fmt.Printf("Butterfish requires an OpenAI API key, please visit https://beta.openai.com/account/api-keys to create one and paste it below (it should start with sk-):\n")

	// read in the token and validate
	fmt.Scanln(&token)
	token = strings.TrimSpace(token)
	if token == "" {
		log.Fatal("No token provided, exiting")
	}
	if !strings.HasPrefix(token, "sk-") {
		log.Fatal("Invalid token provided, exiting")
	}

	// attempt to write a .env file
	fmt.Printf("\nSaving token to %s\n", path)
	err := os.MkdirAll(filepath.Dir(path), 0755)
	if err != nil {
		fmt.Printf("Error creating directory: %s\n", err.Error())
		return token
	}

	envFile, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		fmt.Printf("Error creating file: %s\n", err.Error())
		return token
	}
	defer envFile.Close()

	content := fmt.Sprintf("OPENAI_TOKEN=%s\n", token)
	_, err = envFile.WriteString(content)
	if err != nil {
		fmt.Printf("Error writing file: %s\n", err.Error())
	}

	fmt.Printf("Token saved, you can edit it at any time at %s\n\n", path)

	return token

}

// Kong configuration for shell arguments (shell meaning when butterfish is
// invoked, rather than when we're inside a butterfish console).
// Kong will parse os.Args based on this struct.
type cliShell struct {
	Verbose bool `short:"v" default:"false" help:"Verbose mode, prints full LLM prompts."`

	Wrap struct {
		Cmd string `arg:"" help:"Command to wrap (e.g. zsh)"`
	} `cmd:"" help:"Wrap a command (e.g. zsh) to expose to Butterfish."`

	Console struct {
	} `cmd:"" help:"Start a Butterfish console and server."`

	// We include the cliConsole options here so that we can parse them and hand them
	// to the console executor, even though we're in the shell context here
	cliConsole
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

func makeButterfishConfig(options *cliShell) *ButterfishConfig {
	colorScheme := &gruvboxDark

	promptPath := "~/.config/butterfish/prompts.yaml"
	promptPath, err := homedir.Expand(promptPath)
	if err != nil {
		log.Fatal(err)
	}

	return &ButterfishConfig{
		Verbose:            options.Verbose,
		OpenAIToken:        getOpenAIToken(),
		ColorScheme:        colorScheme,
		Styles:             colorSchemeToStyles(colorScheme),
		PromptLibraryPath:  promptPath,
		SummarizeChunkSize: 3600, // This generally fits into 4096 token limits
		SummarizeMaxChunks: 8,    // Summarize 8 chunks before bailing
	}
}

const description = `Do useful things with LLMs from the command line, with a bent towards software engineering.`

const license = "MIT License - Copyright (c) 2023 Peter Bakkum"

var ( // these are filled in at build time
	BuildVersion   string
	BuildArch      string
	BuildCommit    string
	BuildOs        string
	BuildTimestamp string
)

func getBuildInfo() string {
	return fmt.Sprintf("%s %s %s\n(commit %s) (built %s)\n%s\n", BuildVersion, BuildOs, BuildArch, BuildCommit, BuildTimestamp, license)
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

func main() {
	desc := fmt.Sprintf("%s\n%s", description, getBuildInfo())
	cli := &cliShell{}

	cliParser, err := kong.New(cli,
		kong.Name("butterfish"),
		kong.Description(desc),
		kong.UsageOnError())
	if err != nil {
		panic(err)
	}

	parsedCmd, err := cliParser.Parse(os.Args[1:])
	cliParser.FatalIfErrorf(err)

	config := makeButterfishConfig(cli)
	ctx, cancel := context.WithCancel(context.Background())

	errorWriter := util.NewStyledWriter(os.Stderr, config.Styles.Error)

	// There are special commands (console and wrap) which are interpreted here,
	// the rest are intepreted in commands.go
	switch parsedCmd.Command() {
	case "wrap <cmd>":
		client, err := runIPCClient(ctx)
		if err != nil {
			fmt.Fprintf(errorWriter, err.Error())
			os.Exit(2)
		}

		cmdArr := os.Args[2:]
		err = wrapCommand(ctx, cancel, cmdArr, client) // this is blocking
		if err != nil {
			fmt.Fprintf(errorWriter, err.Error())
			os.Exit(3)
		}

	case "console":
		initLogging(ctx)

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
			fmt.Fprintf(errorWriter, err.Error())
			os.Exit(1)
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

	default:
		verboseWriter := util.NewStyledWriter(os.Stdout, config.Styles.Grey)
		gpt := NewGPT(config.OpenAIToken, config.Verbose, verboseWriter)

		promptLibrary, err := initializePrompts(config, verboseWriter)
		if err != nil {
			fmt.Fprintf(errorWriter, err.Error())
			os.Exit(1)
		}

		butterfishCtx := ButterfishCtx{
			ctx:           ctx,
			cancel:        cancel,
			promptLibrary: promptLibrary,
			inConsoleMode: false,
			config:        config,
			gptClient:     gpt,
			out:           os.Stdout,
		}

		err = butterfishCtx.ExecCommand(parsedCmd, &cli.cliConsole)

		if err != nil {
			butterfishCtx.StylePrintf(config.Styles.Error, "Error: %s\n", err.Error())
			os.Exit(4)
		}
	}
}
