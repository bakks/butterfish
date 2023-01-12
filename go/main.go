package main

import (
	"context"
	"encoding/binary"
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

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/lipgloss"
	"github.com/creack/pty"
	"github.com/joho/godotenv"
	"golang.org/x/term"

	"github.com/bakks/butterfish/go/charmcomponents/console"
	"github.com/bakks/butterfish/proto"
)

// Main driver for the Butterfish set of command line tools. These are tools
// for using AI capabilities on the command line.

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

// String ANSI tty control codes out of a string
func stripANSI(str string) string {
	return ansiRegexp.ReplaceAllString(str, "")
}

// Function for filtering out non-printable characters from a string
func filterNonPrintable(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\t':
			return r

		default:
			if unicode.IsPrint(r) {
				return r
			}
			return -1
		}
	}, s)
}

func sanitizeOutputString(output string) string {
	return filterNonPrintable(stripANSI(output))
}

// We're multiplexing here between the stdin/stdout of this
// process, stdin/stdout of the wrapped process, and
// input/output of the connection to the butterfish server.
func wrappingMultiplexer(
	ctx context.Context,
	cancel context.CancelFunc,
	remoteClient proto.Butterfish_StreamBlocksClient,
	childIn io.Writer,
	parentIn, remoteIn, childOut <-chan *byteMsg) {

	for {
		select {

		// Receive data from this process's stdin and write it only
		// to the child process' stdin
		case s1 := <-parentIn:
			_, err := fmt.Fprint(childIn, string(s1.Data))
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
			binary.Write(os.Stdout, binary.LittleEndian, s2.Data)

			// Filter out characters and send to server
			printedStr := sanitizeOutputString(string(s2.Data))
			err := remoteClient.Send(&proto.StreamBlock{Data: []byte(printedStr)})
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
func wrapCommand(ctx context.Context, cancel context.CancelFunc, command []string, client proto.Butterfish_StreamBlocksClient) error {
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

	wrappingMultiplexer(ctx, cancel, client, ptmx, parentIn, remoteIn, childOut)

	return nil
}

const GPTMaxTokens = 1024

const shellOutPrompt = `The following is output from a user running a shell command, if it contains an error then print the specific segment that is an error and explain briefly how to solve the error, otherwise respond with only "NOOP". "%s"`

const summarizePrompt = `The following is a raw text file with path "%s", summarize the file contents, the file's purpose, and write a list of the file's key elements:
'''
%s
'''

Summary:`

const summarizeFactsPrompt = `The following is a raw text file with path "%s", write a bullet-point list of facts from the document starting with the most important.
'''
%s
'''

Summary:`

const summarizeListOfFactsPrompt = `The following is a list of facts about file "%s", write a description of the file and summarize its important facts in a bulleted list.
'''
%s
'''

Description and Important Facts:`

const gencmdPrompt = `Write a shell command that accomplishes the following goal. Respond with only the shell command.
'''
%s
'''

Shell command:`

var questionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
var summarizeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
var errorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("198"))

// Clean up the cmd string and return a lowercased first word in it
func getMiniCmd(cmd string) string {
	cmd = strings.Split(cmd, " ")[0]
	cmd = strings.ToLower(cmd)
	return cmd
}

var availableCommands = []string{
	"prompt", "help", "summarize", "exit",
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
func (this *ButterfishCtx) summarizePath(path string) error {
	// open file
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	const bytesPerChunk = 3800
	const maxChunks = 8
	buffer := make([]byte, bytesPerChunk)

	// read file
	bytesRead, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return err
	}

	fmt.Fprintf(this.out, "Summarizing %s\n", path)

	if bytesRead < bytesPerChunk {
		// the entire document fits within the token limit, summarize directly
		prompt := fmt.Sprintf(summarizePrompt, path, string(buffer))
		//fmt.Fprintf(console, prompt)
		err = this.gptClient.CompletionStream(this.ctx, prompt, this.out, summarizeStyle)
		if err != nil {
			return err
		}
	} else {
		// the document doesn't fit within the token limit, we'll iterate over it
		// and summarize each chunk as facts, then ask for a summary of facts

		facts := strings.Builder{}

		for i := 0; i < maxChunks; i++ {
			prompt := fmt.Sprintf(summarizeFactsPrompt, path, string(buffer))
			resp, err := this.gptClient.Completion(this.ctx, prompt, this.out)
			if err != nil {
				return err
			}
			facts.WriteString(resp)
			fmt.Fprintf(this.out, "Chunk %d: %s", i, resp)

			bytesRead, err := file.Read(buffer)
			if err != nil && err != io.EOF {
				return err
			}

			if bytesRead < 16 {
				break
			}
		}

		mergedFacts := facts.String()
		prompt := fmt.Sprintf(summarizeListOfFactsPrompt, path, mergedFacts)
		fmt.Fprintf(this.out, prompt)
		err = this.gptClient.CompletionStream(this.ctx, prompt, this.out, summarizeStyle)

	}

	return nil
}

// Iterate through a list of file paths and summarize each
func (this *ButterfishCtx) summarizeCommand(paths []string) error {
	for _, path := range paths {
		err := this.summarizePath(path)
		if err != nil {
			return err
		}
	}

	return nil
}

// Given a description of functionality, we call GPT to generate a shell
// command
func (this *ButterfishCtx) gencmdCommand(description string) error {
	prompt := fmt.Sprintf(gencmdPrompt, description)
	resp, err := this.gptClient.Completion(this.ctx, prompt, this.out)
	if err != nil {
		return err
	}

	this.updateCommandRegister(resp)
	fmt.Fprintf(this.out, "Run exec or execremote to execute.")
	return nil
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

// Execute the command stored in commandRegister on the remote host
func (this *ButterfishCtx) execremoteCommand(cmd string) error {
	if cmd == "" && this.commandRegister == "" {
		return errors.New("No command to execute")
	}
	if cmd == "" {
		cmd = this.commandRegister
	}
	cmd += "\n"

	fmt.Fprintf(this.out, "Executing: %s\n", cmd)
	return this.clientController.Write(0, cmd)
}

func (this *ButterfishCtx) updateCommandRegister(cmd string) {
	cmd = strings.TrimSpace(cmd)
	this.commandRegister = cmd
	fmt.Fprintf(this.out, "Command register updated to:\n %s\n", cmd)
}

type ButterfishCtx struct {
	ctx              context.Context  // global context, should be passed through to other calls
	gptClient        *GPT             // GPT client
	out              io.Writer        // output writer
	commandRegister  string           // landing space for generated commands
	consoleCmdChan   <-chan string    // channel for console commands
	clientController ClientController // client controller
}

// A function to handle a cmd string when received from consoleCommand channel
func (this *ButterfishCtx) handleConsoleCommand(cmd string) error {
	cmd = strings.TrimSpace(cmd)
	miniCmd := getMiniCmd(cmd)

	switch miniCmd {
	case "exit":
		fmt.Fprintf(this.out, "Exiting...")

	case "help":
		fmt.Fprintf(this.out, "Available commands: %s", strings.Join(availableCommands, ", "))

	case "summarize":
		fields := strings.Fields(cmd)
		if len(fields) < 2 {
			fmt.Fprintf(this.out, "Please provide a file path to summarize")
			break
		}

		err := this.summarizeCommand(fields[1:])
		return err

	case "gencmd":
		fields := strings.Split(cmd, " ")
		if len(fields) < 2 {
			return errors.New("Please provide a description to generate a command")
		}

		description := strings.Join(fields[1:], " ")
		err := this.gencmdCommand(description)
		return err

	case "execremote":
		fields := strings.Fields(cmd)

		if this.commandRegister == "" && len(fields) == 1 {
			return errors.New("No command to execute")
		}

		var execCmd string
		if len(fields) > 1 {
			execCmd = strings.Join(fields[1:], " ")
		}
		this.execremoteCommand(execCmd)

	case "exec":
		fields := strings.Fields(cmd)
		if this.commandRegister == "" && len(fields) == 1 {
			return errors.New("No command to execute")
		}

		var execCmd string
		if len(fields) > 1 {
			execCmd = strings.Join(fields[1:], " ")
		}
		this.execCommand(execCmd)

	default:
		return this.gptClient.CompletionStream(this.ctx, cmd, this.out, questionStyle)

	}

	return nil
}

func (this *ButterfishCtx) serverMultiplexer() {
	clientInput := this.clientController.GetReader()
	fmt.Fprintln(this.out, "Butterfish server active...")

	for {
		select {
		case cmd := <-this.consoleCmdChan:
			err := this.handleConsoleCommand(cmd)
			if err != nil {
				fmt.Fprintf(this.out, "Error: %v", err)
				continue
			}

		case clientData := <-clientInput:
			// if the client data message is greater than 35 runes then we call GPT
			// for error detection and solutions
			// TODO this is a very dumb predicate
			if len(clientData.Data) < 35 {
				continue
			}

			cmd := fmt.Sprintf(shellOutPrompt, clientData.Data)
			err := this.gptClient.CompletionStream(this.ctx, cmd, this.out, errorStyle)
			if err != nil {
				fmt.Fprintf(this.out, "Error: %v", err)
				continue
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

func newButterfishCtx(ctx context.Context, config *butterfishConfig) *ButterfishCtx {
	gpt := getGPTClient(config.Verbose)

	return &ButterfishCtx{
		ctx:       ctx,
		gptClient: gpt,
		out:       os.Stdout,
	}
}

func makeButterfishConfig() *butterfishConfig {
	return &butterfishConfig{
		Verbose: cli.Verbose,
	}
}

func getGPTClient(verbose bool) *GPT {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("You need a .env file in the current directory that defines OPENAI_TOKEN.\ne.g. OPENAI_TOKEN=foobar")
	}

	// initialize GPT API client
	token := os.Getenv("OPENAI_TOKEN")
	return NewGPT(token, verbose)
}

// Kong CLI parser option configuration
var cli struct {
	Verbose bool `short:"v" default:"false" help:"Verbose mode, prints full LLM prompts."`

	//Help struct{} `cmd:"" short:"h" help:"Show help."`

	Wrap struct {
		Cmd string `arg:"" help:"Command to wrap (e.g. zsh)"`
	} `cmd:"" help:"Wrap a command (e.g. zsh) to expose to Butterfish."`

	Console struct {
	} `cmd:"" help:"Start a Butterfish console and server."`

	Prompt struct {
		Prompt string `arg:"" help:"Prompt to use."`
		Model  string `short:"m" default:"text-davinci-003" help:"GPT model to use for the prompt."`
	} `cmd:"" help:"Run a specific GPT prompt, print results, and exit."`

	Summarize struct {
		Files []string `arg:"" help:"File paths to summarize."`
	} `cmd:"" help:"Semantically summarize a list of files."`
}

type butterfishConfig struct {
	Verbose bool
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

	kongCtx := kong.Parse(&cli,
		kong.Name("butterfish"),
		kong.Description(desc),
		kong.UsageOnError())

	config := makeButterfishConfig()
	ctx, cancel := context.WithCancel(context.Background())

	switch kongCtx.Command() {
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
		gpt := getGPTClient(config.Verbose)

		// initialize console UI
		consoleCommand := make(chan string)
		cmdCallback := func(cmd string) {
			consoleCommand <- cmd
		}
		exitCallback := func() {
			cancel()
		}
		cons := console.NewConsoleProgram(cmdCallback, exitCallback)

		clientController := runIPCServer(ctx, cons)

		butterfishCtx := ButterfishCtx{
			ctx:              ctx,
			gptClient:        gpt,
			out:              cons,
			consoleCmdChan:   consoleCommand,
			clientController: clientController,
		}

		// this is blocking
		butterfishCtx.serverMultiplexer()

	case "prompt <prompt>":
		gpt := getGPTClient(config.Verbose)
		err := gpt.CompletionStream(ctx, cli.Prompt.Prompt, os.Stdout, questionStyle)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

	case "summarize <files>":
		butterfish := newButterfishCtx(ctx, config)
		err := butterfish.summarizeCommand(cli.Summarize.Files)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

	default:
		fmt.Printf("Unknown command: %s", kongCtx.Command())
		os.Exit(1)
	}
}
