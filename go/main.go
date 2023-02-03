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
	"github.com/spf13/afero"
	"golang.org/x/term"

	"github.com/bakks/butterfish/go/charmcomponents/console"
	"github.com/bakks/butterfish/go/embedding"
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

// An implementation of io.Writer that renders output with a lipgloss style
// and filters out the special token "NOOP". This is specially handled -
// we seem to get "NO" as a separate token from GPT.
type StyledWriter struct {
	Writer io.Writer
	Style  lipgloss.Style
	cache  []byte
}

// Writer for StyledWriter
// This is a bit insane but it's a dumb way to filter out NOOP split into
// two tokens, should probably be rewritten
func (this *StyledWriter) Write(p []byte) (n int, err error) {
	if string(p) == "NOOP" {
		// This doesn't seem to actually happen since it gets split into two
		// tokens? but let's code defensively
		return len(p), nil
	}

	if string(p) == "NO" {
		this.cache = p
		return len(p), nil
	}
	if string(p) == "OP" && this.cache != nil {
		// We have a NOOP, discard it
		this.cache = nil
		return len(p), nil
	}

	if this.cache != nil {
		p = append(this.cache, p...)
		this.cache = nil
	}

	str := string(p)
	str = strings.ReplaceAll(str, "\x04", "\n") // replace EOF with a newline
	rendered := this.Style.Render(str)
	b := []byte(rendered)

	_, err = this.Writer.Write(b)
	if err != nil {
		return 0, err
	}
	// use len(p) rather than len(b) because it would be unexpected to get
	// a different number of bytes written than were passed in, (lipgloss
	// render adds ANSI codes)
	return len(p), nil
}

func NewStyledWriter(writer io.Writer, style lipgloss.Style) *StyledWriter {
	return &StyledWriter{
		Writer: writer,
		Style:  style,
	}
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

const shellOutPrompt = `The following is output from a user inside a "%s" shell, the user ran the command "%s", if the output contains an error then print the specific segment that is an error and explain briefly how to solve the error, otherwise respond with only "NOOP". "%s"`

const summarizePrompt = `The following is a raw text file, summarize the file contents, the file's purpose, and write a list of the file's key elements:
'''
%s
'''

Summary:`

const summarizeFactsPrompt = `The following is a raw text file, write a bullet-point list of facts from the document starting with the most important.
'''
%s
'''

Summary:`

const summarizeListOfFactsPrompt = `The following is a list of facts, write a general description of the document and summarize its important facts in a bulleted list.
'''
%s
'''

Description and Important Facts:`

const gencmdPrompt = `Write a shell command that accomplishes the following goal. Respond with only the shell command.
'''
%s
'''

Shell command:`

const questionPrompt = `Answer this question about a file:"%s". Here are some snippets from the file separated by '---'.
'''
%s
'''`

func (this *ButterfishCtx) CalculateEmbeddings(ctx context.Context, content []string) ([][]float64, error) {
	return this.gptClient.Embeddings(ctx, content)
}

// A local printf that writes to the butterfishctx out using a lipgloss style
func (this *ButterfishCtx) StylePrintf(style lipgloss.Style, format string, a ...any) {
	fmt.Fprintf(this.out, style.Render(format), a...)
}

func (this *ButterfishCtx) Printf(format string, a ...any) {
	fmt.Fprintf(this.out, this.config.Styles.Foreground.Render(format), a...)
}

func (this *ButterfishCtx) SummarizeChunks(chunks [][]byte) error {
	writer := NewStyledWriter(this.out, this.config.Styles.Answer)

	if len(chunks) == 1 {
		// the entire document fits within the token limit, summarize directly
		prompt := fmt.Sprintf(summarizePrompt, string(chunks[0]))
		return this.gptClient.CompletionStream(this.ctx, prompt, "", writer)
	}

	// the document doesn't fit within the token limit, we'll iterate over it
	// and summarize each chunk as facts, then ask for a summary of facts
	facts := strings.Builder{}

	for _, chunk := range chunks {
		if len(chunk) < 16 {
			break
		}

		prompt := fmt.Sprintf(summarizeFactsPrompt, string(chunk))
		resp, err := this.gptClient.Completion(this.ctx, prompt, this.out)
		if err != nil {
			return err
		}
		facts.WriteString(resp)
		facts.WriteString("\n")
	}

	mergedFacts := facts.String()
	prompt := fmt.Sprintf(summarizeListOfFactsPrompt, mergedFacts)
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
	const bytesPerChunk = 3800
	const maxChunks = 8

	this.StylePrintf(this.config.Styles.Question, "Summarizing %s\n", path)
	chunks := [][]byte{}

	fs := afero.NewOsFs()
	util.ChunkFile(fs, path, bytesPerChunk, maxChunks, func(i int, buf []byte) error {
		chunks = append(chunks, buf)
		return nil
	})

	return this.SummarizeChunks(chunks)
}

// Iterate through a list of file paths and summarize each
func (this *ButterfishCtx) summarizePaths(paths []string) error {
	for _, path := range paths {
		err := this.SummarizePath(path)
		if err != nil {
			return err
		}
	}

	return nil
}

// Given a description of functionality, we call GPT to generate a shell
// command
func (this *ButterfishCtx) gencmdCommand(description string) (string, error) {
	prompt := fmt.Sprintf(gencmdPrompt, description)
	resp, err := this.gptClient.Completion(this.ctx, prompt, this.out)
	if err != nil {
		return "", err
	}

	this.updateCommandRegister(resp)
	fmt.Fprintf(this.out, "Run exec or execremote to execute\n")
	return resp, nil
}

// Function that executes a command on the local host as a child and streams
// the stdout/stderr to a writer. If the context is cancelled then the child
// process is killed.
func executeCommand(ctx context.Context, cmd string, out io.Writer) error {
	c := exec.CommandContext(ctx, "/bin/sh", "-c", cmd)
	c.Stdout = out
	c.Stderr = out
	return c.Run()
}

// Execute the command as a child of this process (rather than a remote
// process), either from the command register or from a command string
func (this *ButterfishCtx) execCommand(cmd string) error {
	if cmd == "" && this.commandRegister == "" {
		return errors.New("No command to execute")
	}
	if cmd == "" {
		cmd = this.commandRegister
	}

	fmt.Fprintf(this.out, "Executing: %s\n", cmd)
	return executeCommand(this.ctx, cmd, this.out)
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
	cmd = strings.TrimSpace(cmd)
	this.commandRegister = cmd
	fmt.Fprintf(this.out, "Command register updated to:\n %s\n", cmd)
}

type ButterfishCtx struct {
	ctx              context.Context              // global context, should be passed through to other calls
	cancel           context.CancelFunc           // cancel function for the global context
	inConsoleMode    bool                         // true if we're running in console mode
	config           *butterfishConfig            // configuration
	gptClient        *GPT                         // GPT client
	out              io.Writer                    // output writer
	commandRegister  string                       // landing space for generated commands
	consoleCmdChan   <-chan string                // channel for console commands
	clientController ClientController             // client controller
	vectorIndex      embedding.FileEmbeddingIndex // embedding index for searching local files
}

// Ensure we have a vector index object, idempotent
func (this *ButterfishCtx) loadVectorIndex() {
	if this.vectorIndex != nil {
		return
	}

	index := embedding.NewDiskCachedEmbeddingIndex()
	if this.config.Verbose {
		index.SetOutput(this.out)
	}

	this.vectorIndex = index
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
	prompt := fmt.Sprintf(shellOutPrompt, openCmd, lastCmd, output)
	writer := NewStyledWriter(this.out, this.config.Styles.Error)
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
	Prompt     lipgloss.Style
	Error      lipgloss.Style
	Foreground lipgloss.Style
	Grey       lipgloss.Style
}

func colorSchemeToStyles(colorScheme *ColorScheme) *styles {
	return &styles{
		Question:   lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Color3)),
		Answer:     lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Color2)),
		Summarize:  lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Color2)),
		Prompt:     lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Color4)),
		Error:      lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Error)),
		Foreground: lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Foreground)),
		Grey:       lipgloss.NewStyle().Foreground(lipgloss.Color(colorScheme.Grey)),
	}
}

func makeButterfishConfig(options *cliShell) *butterfishConfig {
	colorScheme := &gruvboxDark

	return &butterfishConfig{
		Verbose:     options.Verbose,
		OpenAIToken: getOpenAIToken(),
		ColorScheme: colorScheme,
		Styles:      colorSchemeToStyles(colorScheme),
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

type butterfishConfig struct {
	Verbose     bool
	OpenAIToken string
	ColorScheme *ColorScheme
	Styles      *styles
}

const description = `Let's do useful things with LLMs from the command line, with a bent towards software engineering.`

const license = "MIT License - Copyright (c) 2023 Peter Bakkum"

var ( // these are filled in at build time
	BuildVersion   string
	BuildArch      string
	BuildCommit    string
	BuildOs        string
	BuildTimestamp string
)

func getBuildInfo() string {
	return fmt.Sprintf("%s %s %s (commit %s) (built %s)\n%s\n", BuildVersion, BuildOs, BuildArch, BuildCommit, BuildTimestamp, license)
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

	switch parsedCmd.Command() {
	case "wrap <cmd>":
		client, err := runIPCClient(ctx)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		cmdArr := os.Args[2:]
		err = wrapCommand(ctx, cancel, cmdArr, client) // this is blocking
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
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

		verboseWriter := NewStyledWriter(cons, config.Styles.Foreground)
		gpt := NewGPT(config.OpenAIToken, config.Verbose, verboseWriter)

		clientController := RunIPCServer(ctx, cons)

		butterfishCtx := ButterfishCtx{
			ctx:              ctx,
			cancel:           cancel,
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
		verboseWriter := NewStyledWriter(os.Stdout, config.Styles.Foreground)
		gpt := NewGPT(config.OpenAIToken, config.Verbose, verboseWriter)
		butterfishCtx := ButterfishCtx{
			ctx:           ctx,
			cancel:        cancel,
			inConsoleMode: false,
			config:        config,
			gptClient:     gpt,
			out:           os.Stdout,
		}

		err := butterfishCtx.ExecCommand(parsedCmd, &cli.cliConsole)

		if err != nil {
			log.Fatalf("Error: %s\n", err.Error())
		}
	}
}
