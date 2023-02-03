package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	"github.com/alecthomas/kong"
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
		Paths []string `arg:"" help:"Paths to load into the index."`
	} `cmd:"" help:"Load paths into the index."`

	Indexsearch struct {
		Query string `arg:"" help:"Query to search for."`
	} `cmd:"" help:"Search embedding index and return relevant file snippets."`

	Indexquestion struct {
		Question string `arg:"" help:"Question to ask."`
		Model    string `short:"m" default:"text-davinci-003" help:"GPT model to use for the prompt."`
	} `cmd:"" help:"Ask a question of the index."`
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
		fields := options.Summarize.Files
		if len(fields) == 0 {
			return errors.New("Please provide file paths or piped data to summarize")
		}

		err := this.SummarizePaths(fields)
		return err

	case "gencmd <prompt>":
		input := this.cleanInput(options.Gencmd.Prompt)
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

		prompt := fmt.Sprintf(questionPrompt, input, exerpts)
		err = this.gptClient.CompletionStream(this.ctx, prompt, model, this.out)
		return err

	default:
		return errors.New("Unrecognized command: " + parsed.Command())

	}

	return nil
}
