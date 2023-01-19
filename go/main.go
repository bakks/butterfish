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
	"golang.org/x/term"

	"github.com/bakks/butterfish/go/charmcomponents/console"
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

const questionPrompt = `Answer this question about a file:"%s". Here are some snippets from the file separated by '---'.
'''
%s
'''`

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
	"prompt", "gencmd", "exec", "remoteexec", "help", "summarize", "exit",
}

// Read a local file, break into chunks of a given number of bytes,
// call the callback for each chunk
func chunkFile(
	path string,
	chunkSize uint64,
	maxChunks int,
	callback func(int, []byte) error) error {

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	buf := make([]byte, chunkSize)
	for i := 0; i < maxChunks || maxChunks == -1; i++ {
		n, err := f.Read(buf)
		if err != nil && err != io.EOF {
			return err
		}
		if n == 0 {
			break
		}

		err = callback(i, buf[:n])
		if err != nil {
			return err
		}
	}

	return nil
}

// EmbedFile takes a path to a file, splits the file into chunks, and calls
// the embedding API for each chunk
func (this *ButterfishCtx) EmbedFile(path string) ([]*AnnotatedVector, error) {
	const chunkSize uint64 = 768
	const chunksPerCall = 8
	const maxChunks = 8 * 128

	chunks := []string{}
	annotatedVectors := []*AnnotatedVector{}

	// first we chunk the file
	err := chunkFile(path, chunkSize, maxChunks, func(i int, chunk []byte) error {
		chunks = append(chunks, string(chunk))
		return nil
	})

	if err != nil {
		return nil, err
	}

	// then we call the embedding API for each block of chunks
	for i := 0; i < len(chunks); i += chunksPerCall {
		callChunks := chunks[i:min(i+chunksPerCall, len(chunks))]
		newEmbeddings, err := this.gptClient.Embeddings(this.ctx, callChunks)
		if err != nil {
			return nil, err
		}

		// iterate through response, create an annotation, and create an annotated vector
		for j, embedding := range newEmbeddings {
			rangeStart := uint64(i+j) * chunkSize
			rangeEnd := rangeStart + uint64(len(callChunks[j]))
			absPath, err := filepath.Abs(path)
			if err != nil {
				return nil, err
			}

			av := NewAnnotatedVector(absPath, rangeStart, rangeEnd, embedding)
			if err != nil {
				return nil, err
			}
			annotatedVectors = append(annotatedVectors, av)
		}
	}

	return annotatedVectors, nil
}

// searchFileChunks gets the embeddings for the searchStr using the GPT API,
// searches the vector index for the closest matches, then returns the notes
// associated with those vectors
func (this *ButterfishCtx) searchFileChunks(searchStr string) ([]*ScoredEmbedding, error) {
	const searchLimit = 3
	// call embedding API with search string
	embeddings, err := this.gptClient.Embeddings(this.ctx, []string{searchStr})
	if err != nil {
		return nil, err
	}

	// search the index for the closest matches
	return this.vectorIndex.SearchRaw(embeddings[0], searchLimit)
}

func fetchFileChunks(embeddings []*ScoredEmbedding) ([]string, error) {
	chunks := []string{}

	for _, embedding := range embeddings {
		// read the file
		f, err := os.Open(embedding.Embedding.Name)
		if err != nil {
			return nil, err
		}
		defer f.Close()

		start := embedding.Embedding.Start
		end := embedding.Embedding.End

		// seek to the start byte
		_, err = f.Seek(int64(start), 0)
		if err != nil {
			return nil, err
		}

		// read the chunk
		buf := make([]byte, end-start)
		_, err = f.Read(buf)
		if err != nil {
			return nil, err
		}

		// add the chunk to the map
		chunks = append(chunks, string(buf))
	}

	return chunks, nil
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
	const bytesPerChunk = 3800
	const maxChunks = 8

	fmt.Fprintf(this.out, "Summarizing %s\n", path)
	writer := NewStyledWriter(this.out, summarizeStyle)
	chunks := [][]byte{}

	chunkFile(path, bytesPerChunk, maxChunks, func(i int, buf []byte) error {
		chunks = append(chunks, buf)
		return nil
	})

	if len(chunks) == 1 {
		// the entire document fits within the token limit, summarize directly
		prompt := fmt.Sprintf(summarizePrompt, path, string(chunks[0]))
		return this.gptClient.CompletionStream(this.ctx, prompt, writer)
	}

	// the document doesn't fit within the token limit, we'll iterate over it
	// and summarize each chunk as facts, then ask for a summary of facts
	facts := strings.Builder{}

	for _, chunk := range chunks {
		if len(chunk) < 16 {
			break
		}

		prompt := fmt.Sprintf(summarizeFactsPrompt, path, string(chunk))
		resp, err := this.gptClient.Completion(this.ctx, prompt, this.out)
		if err != nil {
			return err
		}
		facts.WriteString(resp)
		facts.WriteString("\n")
	}

	mergedFacts := facts.String()
	prompt := fmt.Sprintf(summarizeListOfFactsPrompt, path, mergedFacts)
	return this.gptClient.CompletionStream(this.ctx, prompt, writer)
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
	ctx              context.Context   // global context, should be passed through to other calls
	config           *butterfishConfig // configuration
	gptClient        *GPT              // GPT client
	out              io.Writer         // output writer
	commandRegister  string            // landing space for generated commands
	consoleCmdChan   <-chan string     // channel for console commands
	clientController ClientController  // client controller
	vectorIndex      *VectorIndex      // in-memory vector index
}

func (this *ButterfishCtx) loadVectorIndex() {
	this.vectorIndex = NewVectorIndex()
}

// A function to handle a cmd string when received from consoleCommand channel
func (this *ButterfishCtx) handleConsoleCommand(cmd string) (bool, error) {
	cmd = strings.TrimSpace(cmd)
	miniCmd := getMiniCmd(cmd)
	txt := ""
	fields := strings.Fields(cmd)
	if len(fields) > 1 {
		txt = strings.Join(fields[1:], " ")
	}

	switch miniCmd {
	case "exit":
		fmt.Fprintf(this.out, "Exiting...")
		return true, nil

	case "help":
		fmt.Fprintf(this.out, "Available commands: %s", strings.Join(availableCommands, ", "))

	case "summarize":
		fields := strings.Fields(cmd)
		if len(fields) < 2 {
			fmt.Fprintf(this.out, "Please provide a file path to summarize")
			break
		}

		err := this.summarizeCommand(fields[1:])
		return false, err

	case "gencmd":
		fields := strings.Split(cmd, " ")
		if len(fields) < 2 {
			return false, errors.New("Please provide a description to generate a command")
		}

		description := strings.Join(fields[1:], " ")
		_, err := this.gencmdCommand(description)
		return false, err

	case "execremote":
		if this.commandRegister == "" && txt == "" {
			return false, errors.New("No command to execute")
		}

		return false, this.execremoteCommand(txt)

	case "exec":
		if this.commandRegister == "" && txt == "" {
			return false, errors.New("No command to execute")
		}

		return false, this.execCommand(txt)

	case "prompt":
		if txt == "" {
			return false, errors.New("Please provide a prompt")
		}

		writer := NewStyledWriter(this.out, questionStyle)
		return false, this.gptClient.CompletionStream(this.ctx, txt, writer)

	case "index":
		if txt == "" {
			return false, errors.New("Please provide file(s) to index")
		}

		if this.vectorIndex == nil {
			this.loadVectorIndex()
		}

		this.vectorIndex.LoadPaths(fields[1:])
		this.vectorIndex.SetEmbedder(this)
		err := this.vectorIndex.UpdatePaths(fields[1:], false)
		return false, err

	case "vecsearch":
		if txt == "" {
			return false, errors.New("Please provide search parameters")
		}
		if this.vectorIndex == nil {
			return false, errors.New("No vector index loaded")
		}

		scores, err := this.searchFileChunks(txt)
		if err != nil {
			return false, err
		}

		fileChunks, err := fetchFileChunks(scores)
		if err != nil {
			return false, err
		}

		for i, fileChunk := range fileChunks {
			fmt.Fprintf(this.out, "%s\n%s\n\n", scores[i].Embedding.Name, fileChunk)
		}

	case "question":
		if txt == "" {
			return false, errors.New("Please provide a question")
		}
		if this.vectorIndex == nil {
			return false, errors.New("No vector index loaded")
		}

		paths, err := this.searchFileChunks(txt)
		if err != nil {
			return false, err
		}

		fileChunks, err := fetchFileChunks(paths)
		if err != nil {
			return false, err
		}

		samples := fileChunks[:min(len(fileChunks), 2)]
		exerpts := strings.Join(samples, "\n---\n")

		prompt := fmt.Sprintf(questionPrompt, txt, exerpts)
		err = this.gptClient.CompletionStream(this.ctx, prompt, this.out)
		return false, err

	default:
		return false, errors.New("Prefix your input with a command, available commands are: " + strings.Join(availableCommands, ", "))

	}

	return false, nil
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
	writer := NewStyledWriter(this.out, errorStyle)
	err = this.gptClient.CompletionStream(this.ctx, prompt, writer)
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
			exit, err := this.handleConsoleCommand(cmd)
			if err != nil {
				this.printError(err)
				continue
			}
			if exit {
				return
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

func newButterfishCtx(ctx context.Context, config *butterfishConfig) *ButterfishCtx {
	gpt := getGPTClient(config.Verbose)

	return &ButterfishCtx{
		ctx:       ctx,
		config:    config,
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

	Gencmd struct {
		Prompt string `arg:"" help:"Prompt describing the desired shell command."`
		Force  bool   `short:"f" default:"false" help:"Execute the command without prompting."`
	} `cmd:"" help:"Generate a shell command from a prompt."`

	Index struct {
	} `cmd:"" help:"Index the current directory."`
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

		clientController := RunIPCServer(ctx, cons)

		butterfishCtx := ButterfishCtx{
			ctx:              ctx,
			config:           config,
			gptClient:        gpt,
			out:              cons,
			consoleCmdChan:   consoleCommand,
			clientController: clientController,
		}

		// this is blocking
		butterfishCtx.serverMultiplexer()

	case "prompt <prompt>":
		gpt := getGPTClient(config.Verbose)
		writer := NewStyledWriter(os.Stdout, errorStyle)
		err := gpt.CompletionStream(ctx, cli.Prompt.Prompt, writer)
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

	case "gencmd <prompt>":
		butterfish := newButterfishCtx(ctx, config)
		cmd, err := butterfish.gencmdCommand(cli.Gencmd.Prompt)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		if !cli.Gencmd.Force {
			fmt.Printf("Generated command:\n %s\n", cmd)
		} else {
			err := butterfish.execCommand(cmd)
			if err != nil {
				fmt.Println(err)
				os.Exit(1)
			}
		}

	case "index":
		os.Exit(0)

	default:
		fmt.Printf("Unknown command: %s", kongCtx.Command())
		os.Exit(1)
	}
}