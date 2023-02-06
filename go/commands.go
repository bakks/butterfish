package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/bakks/butterfish/go/prompt"
	"github.com/bakks/butterfish/go/util"
)

// Parse and execute a command in a butterfish context
func (this *ButterfishCtx) Command(cmd string) error {
	parsed, options, err := this.ParseCommand(cmd)
	if err != nil {
		return err
	}

	err = this.ExecCommand(parsed, options)
	if err != nil {
		return err
	}

	return nil
}

func (this *ButterfishCtx) ParseCommand(cmd string) (*kong.Context, *cliConsole, error) {
	options := &cliConsole{}
	parser, err := kong.New(options)
	if err != nil {
		return nil, nil, err
	}

	fields := strings.Fields(cmd)
	kongCtx, err := parser.Parse(fields)
	return kongCtx, options, err
}

// Kong CLI parser option configuration
type cliConsole struct {
	Prompt struct {
		Prompt []string `arg:"" help:"Prompt to use." optional:""`
		Model  string   `short:"m" default:"text-davinci-003" help:"GPT model to use for the prompt."`
	} `cmd:"" help:"Run an LLM prompt without prompt wrapping, stream results back."`

	Summarize struct {
		Files []string `arg:"" help:"File paths to summarize." optional:""`
	} `cmd:"" help:"Semantically summarize a list of files."`

	Gencmd struct {
		Prompt []string `arg:"" help:"Prompt describing the desired shell command."`
		Force  bool     `short:"f" default:"false" help:"Execute the command without prompting."`
	} `cmd:"" help:"Generate a shell command from a prompt."`

	Rewrite struct {
		Prompt     string `arg:"" help:"Instruction to the model on how to rewrite."`
		Inputfile  string `short:"i" help:"File to rewrite."`
		Outputfile string `short:"o" help:"File to write the rewritten output to."`
		Inplace    bool   `short:"I" help:"Rewrite the input file in place, cannot be set at the same time as the Output file flag."`
	} `cmd:"" help:"Rewrite a file using a prompt, must specify either a file path or provide piped input."`

	Index struct {
		Paths []string `arg:"" help:"Paths to index."`
	} `cmd:"" help:"Index the current directory."`

	Exec struct {
		Command []string `arg:"" help:"Command to execute." optional:""`
	} `cmd:"" help:"Execute a command, either passed in or in command register."`

	Execremote struct {
		Command []string `arg:"" help:"Command to execute." optional:""`
	} `cmd:"" help:"Execute a command in a wrapped shell, either passed in or in command register."`

	Clearindex struct {
		Paths []string `arg:"" help:"Paths to clear from the index."`
	} `cmd:"" help:"Clear paths from the index."`

	Loadindex struct {
		Paths []string `arg:"" help:"Paths to load into the index." optional:""`
	} `cmd:"" help:"Load paths into the index."`

	Showindex struct {
	} `cmd:"" help:"Show which files are present in the loaded index."`

	Indexsearch struct {
		Query string `arg:"" help:"Query to search for."`
	} `cmd:"" help:"Search embedding index and return relevant file snippets."`

	Indexquestion struct {
		Question string `arg:"" help:"Question to ask."`
		Model    string `short:"m" default:"text-davinci-003" help:"GPT model to use for the prompt."`
	} `cmd:"" help:"Ask a question of the index."`
}

func (this *ButterfishCtx) getPipedStdin() string {
	if !this.inConsoleMode && util.IsPipedStdin() {
		stdin, err := ioutil.ReadAll(os.Stdin)
		if err != nil {
			return ""
		}
		return string(stdin)
	}
	return ""
}

// Given a parsed input split into a slice, join the string together
// and remove any leading/trailing quotes
func (this *ButterfishCtx) cleanInput(input []string) string {
	// If we're not in console mode and we have piped data then use that as input
	if !this.inConsoleMode && util.IsPipedStdin() {
		stdin, err := ioutil.ReadAll(os.Stdin)
		if err != nil {
			return ""
		}
		return string(stdin)
	}

	// otherwise we use the input
	if input == nil || len(input) == 0 {
		return ""
	}

	joined := strings.Join(input, " ")
	joined = strings.Trim(joined, "\"'")
	return joined
}

// A function to handle a cmd string when received from consoleCommand channel
func (this *ButterfishCtx) ExecCommand(parsed *kong.Context, options *cliConsole) error {

	switch parsed.Command() {
	case "exit", "quit":
		fmt.Fprintf(this.out, "Exiting...")
		this.cancel()
		return nil

	case "help":
		parsed.Kong.Stdout = this.out
		parsed.PrintUsage(false)

	case "prompt", "prompt <prompt>":
		input := this.cleanInput(options.Prompt.Prompt)
		if input == "" {
			return errors.New("Please provide a prompt")
		}

		writer := NewStyledWriter(this.out, this.config.Styles.Answer)
		model := options.Prompt.Model
		return this.gptClient.CompletionStream(this.ctx, input, model, writer)

	case "summarize":
		chunks, err := util.GetChunks(
			os.Stdin,
			uint64(this.config.SummarizeChunkSize),
			this.config.SummarizeMaxChunks)

		if err != nil {
			return err
		}

		if len(chunks) == 0 {
			return errors.New("No input to summarize")
		}

		return this.SummarizeChunks(chunks)

	case "summarize <files>":
		files := options.Summarize.Files
		if len(files) == 0 {
			return errors.New("Please provide file paths or piped data to summarize")
		}

		err := this.SummarizePaths(files)
		return err

	case "rewrite <prompt>":
		prompt := options.Rewrite.Prompt
		if prompt == "" {
			return errors.New("Please provide a prompt")
		}
		// cannot set Outputfile and Inplace at the same time
		if options.Rewrite.Outputfile != "" && options.Rewrite.Inplace {
			return errors.New("Cannot set both outputfile and inplace flags")
		}

		input := this.getPipedStdin()
		filename := options.Rewrite.Inputfile
		if input != "" && filename != "" {
			return errors.New("Please provide either piped data or a file path, not both")
		}
		if input == "" && filename == "" {
			return errors.New("Please provide a file path or piped data to rewrite")
		}
		if filename != "" {
			// we have a filename but no piped input, read the file
			content, err := ioutil.ReadFile(filename)
			if err != nil {
				return err
			}
			input = string(content)
		}

		edited, err := this.gptClient.Edits(this.ctx, input, prompt, "code-davinci-edit-001")
		if err != nil {
			return err
		}

		outputFile := options.Rewrite.Outputfile
		// if output file is empty then check inplace flag and use input as output
		if outputFile == "" && options.Rewrite.Inplace {
			outputFile = filename
		}

		if outputFile == "" {
			// If there's no output file specified then print edited text
			this.StylePrintf(this.config.Styles.Answer, "%s", edited)
		} else {
			// otherwise we write to the output file
			err = ioutil.WriteFile(outputFile, []byte(edited), 0644)
			if err != nil {
				return err
			}
		}

		return nil

	case "gencmd <prompt>":
		input := this.cleanInput(options.Gencmd.Prompt)
		if input == "" {
			return errors.New("Please provide a description to generate a command")
		}

		cmd, err := this.gencmdCommand(input)
		if err != nil {
			return err
		}

		// trim whitespace
		cmd = strings.TrimSpace(cmd)

		if !options.Gencmd.Force {
			this.StylePrintf(this.config.Styles.Highlight, "%s\n", cmd)
		} else {
			err := this.execCommand(cmd)
			if err != nil {
				return err
			}
		}
		return nil

	case "execremote <command>":
		input := this.cleanInput(options.Execremote.Command)
		if input == "" {
			input = this.commandRegister
		}

		if input == "" {
			return errors.New("No command to execute")
		}

		return this.execremoteCommand(input)

	case "exec <command>":
		input := this.cleanInput(options.Exec.Command)
		if input == "" {
			input = this.commandRegister
		}

		if input == "" {
			return errors.New("No command to execute")
		}

		return this.execCommand(input)

	case "clearindex <paths>":
		this.initVectorIndex(nil)

		paths := options.Clearindex.Paths
		if len(paths) == 0 {
			paths = []string{"."}
		}

		this.vectorIndex.ClearPaths(this.ctx, paths)
		return nil

	case "showindex":
		this.initVectorIndex(nil)

		paths := this.vectorIndex.IndexedFiles()
		for _, path := range paths {
			this.Printf("%s\n", path)
		}

		return nil

	case "loadindex <paths>":
		paths := options.Loadindex.Paths
		if len(paths) == 0 {
			paths = []string{"."}
		}

		this.Printf("Loading indexes (not generating new embeddings) for %s\n", strings.Join(paths, ", "))
		this.initVectorIndex(paths)

		err := this.vectorIndex.LoadPaths(this.ctx, paths)
		if err != nil {
			return err
		}
		this.Printf("Loaded %d files\n", len(this.vectorIndex.IndexedFiles()))

	case "index <paths>":
		paths := options.Index.Paths
		if len(paths) == 0 {
			paths = []string{"."}
		}

		this.Printf("Indexing %s\n", strings.Join(paths, ", "))
		this.initVectorIndex(paths)

		err := this.vectorIndex.LoadPaths(this.ctx, paths)
		if err != nil {
			return err
		}

		err = this.vectorIndex.IndexPaths(this.ctx, paths, false)

		this.Printf("Done, %d files now loaded in the index\n", len(this.vectorIndex.IndexedFiles()))
		return err

	case "indexsearch <query>":
		this.initVectorIndex(nil)

		input := options.Indexsearch.Query
		if input == "" {
			return errors.New("Please provide search parameters")
		}
		if this.vectorIndex == nil {
			return errors.New("No vector index loaded")
		}

		results, err := this.vectorIndex.Search(this.ctx, input, 5)
		if err != nil {
			return err
		}

		for _, result := range results {
			this.StylePrintf(this.config.Styles.Highlight, "%s : %0.4f\n", result.FilePath, result.Score)
			this.Printf("%s\n", result.Content)
		}

	case "indexquestion":
		input := options.Indexquestion.Question
		model := options.Indexquestion.Model

		if input == "" {
			return errors.New("Please provide a question")
		}
		if this.vectorIndex == nil {
			return errors.New("No vector index loaded")
		}

		results, err := this.vectorIndex.Search(this.ctx, input, 3)
		if err != nil {
			return err
		}
		samples := []string{}

		for _, result := range results {
			samples = append(samples, result.Content)
		}

		exerpts := strings.Join(samples, "\n---\n")

		prompt, err := this.promptLibrary.GetPrompt(prompt.PromptQuestion,
			"snippets", exerpts,
			"question", input)
		if err != nil {
			return err
		}
		err = this.gptClient.CompletionStream(this.ctx, prompt, model, this.out)
		return err

	default:
		return errors.New("Unrecognized command: " + parsed.Command())

	}

	return nil
}

// Given a description of functionality, we call GPT to generate a shell
// command
func (this *ButterfishCtx) gencmdCommand(description string) (string, error) {
	prompt, err := this.promptLibrary.GetPrompt("generate_command", "content", description)
	if err != nil {
		return "", err
	}

	resp, err := this.gptClient.Completion(this.ctx, prompt, this.out)
	if err != nil {
		return "", err
	}

	this.updateCommandRegister(resp)
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

	if this.config.Verbose {
		this.StylePrintf(this.config.Styles.Question, "exec> %s\n", cmd)
	}
	return executeCommand(this.ctx, cmd, this.out)
}
