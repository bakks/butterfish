package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/alecthomas/kong"
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

func (this *ButterfishCtx) ParseCommand(cmd string) (*kong.Context, *cliOptions, error) {
	options := &cliOptions{}
	parser, err := kong.New(options)
	if err != nil {
		return nil, nil, err
	}

	fields := strings.Fields(cmd)
	kongCtx, err := parser.Parse(fields)
	return kongCtx, options, err
}

// Kong CLI parser option configuration
type cliOptions struct {
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
		Paths []string `arg:"" help:"Paths to index."`
	} `cmd:"" help:"Index the current directory."`

	Exec struct {
		Command string `arg:"" help:"Command to execute."` // make optional?
	} `cmd:"" help:"Execute a command, either passed in or in command register."`

	Execremote struct {
		Command string `arg:"" help:"Command to execute."` // make optional?
	} `cmd:"" help:"Execute a command in a wrapped shell, either passed in or in command register."`

	Clearindex struct {
		Paths []string `arg:"" help:"Paths to clear from the index."`
	} `cmd:"" help:"Clear paths from the index."`

	Loadindex struct {
		Paths []string `arg:"" help:"Paths to load into the index."`
	} `cmd:"" help:"Load paths into the index."`

	Indexsearch struct {
		Query string `arg:"" help:"Query to search for."`
	} `cmd:"" help:"Search embedding index and return relevant file snippets."`

	Indexquestion struct {
		Question string `arg:"" help:"Question to ask."`
	} `cmd:"" help:"Ask a question of the index."`
}

// A function to handle a cmd string when received from consoleCommand channel
func (this *ButterfishCtx) ExecCommand(parsed *kong.Context, options *cliOptions) error {

	switch parsed.Command() {
	case "exit", "quit":
		fmt.Fprintf(this.out, "Exiting...")
		this.cancel()
		return nil

	case "help":
		fmt.Fprintf(this.out, "TODO: help\n")

	case "summarize <files>":
		fields := options.Summarize.Files
		if len(fields) < 2 {
			fmt.Fprintf(this.out, "Please provide a file path to summarize")
			break
		}

		err := this.summarizeCommand(fields[1:])
		return err

	case "gencmd <prompt>":
		input := options.Gencmd.Prompt
		if input == "" {
			return errors.New("Please provide a description to generate a command")
		}

		cmd, err := this.gencmdCommand(input)
		if err != nil {
			return err
		}

		if !options.Gencmd.Force {
			fmt.Printf("Generated command:\n %s\n", cmd)
		} else {
			err := this.execCommand(cmd)
			if err != nil {
				return err
			}
		}
		return nil

	case "execremote <command>":
		input := options.Execremote.Command
		if input == "" {
			input = this.commandRegister
		}

		if input == "" {
			return errors.New("No command to execute")
		}

		return this.execremoteCommand(input)

	case "exec <command>":
		input := options.Exec.Command
		if input == "" {
			input = this.commandRegister
		}

		if input == "" {
			return errors.New("No command to execute")
		}

		return this.execCommand(input)

	case "prompt <prompt>":
		input := options.Prompt.Prompt
		if input == "" {
			return errors.New("Please provide a prompt")
		}

		writer := NewStyledWriter(this.out, this.config.Styles.Answer)
		return this.gptClient.CompletionStream(this.ctx, input, writer)

	case "clearindex <paths>":
		paths := options.Clearindex.Paths
		if len(paths) == 0 {
			paths = []string{"."}
		}

		this.vectorIndex.ClearPaths(this.ctx, paths)
		return nil

	case "showindex":
		paths := this.vectorIndex.IndexedFiles()
		for _, path := range paths {
			fmt.Fprintf(this.out, "%s\n", path)
		}

		return nil

	case "loadindex <paths>":
		paths := options.Loadindex.Paths
		if len(paths) == 0 {
			paths = []string{"."}
		}

		fmt.Fprintf(this.out, "Loading indexes (not generating new embeddings) for %s\n", strings.Join(paths, ", "))
		this.loadVectorIndex()

		err := this.vectorIndex.LoadPaths(this.ctx, paths)
		if err != nil {
			return err
		}

	case "index <paths>":
		paths := options.Index.Paths
		if len(paths) == 0 {
			paths = []string{"."}
		}

		fmt.Fprintf(this.out, "Indexing %s\n", strings.Join(paths, ", "))

		this.loadVectorIndex()

		err := this.vectorIndex.LoadPaths(this.ctx, paths)
		if err != nil {
			return err
		}

		this.vectorIndex.SetEmbedder(this)
		err = this.vectorIndex.IndexPaths(this.ctx, paths, false)
		return err

	case "indexsearch <query>":
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
			fmt.Fprintf(this.out, "%s\n%s\n\n", result.FilePath, result.Content)
		}

	case "indexquestion":
		input := options.Indexquestion.Question

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

		prompt := fmt.Sprintf(questionPrompt, input, exerpts)
		err = this.gptClient.CompletionStream(this.ctx, prompt, this.out)
		return err

	default:
		return errors.New("Unrecognized command: " + parsed.Command())

	}

	return nil
}
