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
	"syscall"

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
	} `cmd:"" help:"Run an LLM prompt without wrapping, stream results back. Accepts piped input. This is a straight-through call to the LLM from the command line with a given prompt. It is recommended that you wrap the prompt with quotes. This defaults to the text-davinci-003."`

	Summarize struct {
		Files []string `arg:"" help:"File paths to summarize." optional:""`
	} `cmd:"" help:"Semantically summarize a list of files (or piped input). We read in the file, if it is short then we hand it directly to the LLM and ask for a summary. If it is longer then we break it into chunks and ask for a list of facts from each chunk (max 8 chunks), then concatenate facts and ask GPT for an overall summary."`

	Gencmd struct {
		Prompt []string `arg:"" help:"Prompt describing the desired shell command."`
		Force  bool     `short:"f" default:"false" help:"Execute the command without prompting."`
	} `cmd:"" help:"Generate a shell command from a prompt, i.e. pass in what you want, a shell command will be generated. Accepts piped input. You can use the -f command to execute it sight-unseen."`

	Rewrite struct {
		Prompt     string `arg:"" help:"Instruction to the model on how to rewrite."`
		Inputfile  string `short:"i" help:"File to rewrite."`
		Outputfile string `short:"o" help:"File to write the rewritten output to."`
		Inplace    bool   `short:"I" help:"Rewrite the input file in place, cannot be set at the same time as the Output file flag."`
	} `cmd:"" help:"Rewrite a file using a prompt, must specify either a file path or provide piped input, and can output to stdout, output to a given file, or edit the input file in-place."`

	Index struct {
		Paths []string `arg:"" help:"Paths to index." optional:""`
		Force bool     `short:"f" default:"false" help:"Force re-indexing of files rather than skipping cached embeddings."`
	} `cmd:"" help:"Recursively index the current directory using embeddings. This will read each file, split it into chunks, embed the chunks, and write a .butterfish_index file to each directory caching the embeddings. If you re-run this it will skip over previously embedded files unless you force a re-index. This implements an exponential backoff if you hit OpenAI API rate limits."`

	Exec struct {
		Command []string `arg:"" help:"Command to execute." optional:""`
	} `cmd:"" help:"Execute a command and try to debug problems. The command can either passed in or in the command register (if you have run gencmd in Console Mode)."`

	Execremote struct {
		Command []string `arg:"" help:"Command to execute." optional:""`
	} `cmd:"" help:"Execute a command in a wrapped shell, either passed in or in command register. This is specifically for Console Mode after you have run gencmd when you have a wrapped terminal open."`

	Clearindex struct {
		Paths []string `arg:"" help:"Paths to clear from the index." optional:""`
	} `cmd:"" help:"Clear paths from the index, both from the in-memory index (if in Console Mode) and to delete .butterfish_index files."`

	Loadindex struct {
		Paths []string `arg:"" help:"Paths to load into the index." optional:""`
	} `cmd:"" help:"Load paths into the index. This is specifically for Console Mode when you want to load a set of cached indexes into memory."`

	Showindex struct {
		Paths []string `arg:"" help:"Paths to show from the index." optional:""`
	} `cmd:"" help:"Show which files are present in the loaded index. You can pass in a path but it defaults to the current directory."`

	Indexsearch struct {
		Query   string `arg:"" help:"Query to search for."`
		Results int    `short:"r" default:"5" help:"Number of results to return."`
	} `cmd:"" help:"Search embedding index and return relevant file snippets."`

	Indexquestion struct {
		Question string `arg:"" help:"Question to ask."`
		Model    string `short:"m" default:"text-davinci-003" help:"GPT model to use for the prompt."`
	} `cmd:"" help:"Ask a question using the embeddings index. This fetches text snippets from the index and passes them to the LLM to generate an answer, thus you need to run the index command first."`
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

		writer := util.NewStyledWriter(this.out, this.config.Styles.Answer)
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
			_, err := this.execCommand(cmd)
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

	case "exec", "exec <command>":
		input := this.cleanInput(options.Exec.Command)
		if input == "" {
			input = this.commandRegister
		}

		if input == "" {
			return errors.New("No command to execute")
		}

		return this.execAndCheck(this.ctx, input)

	case "clearindex", "clearindex <paths>":
		this.initVectorIndex(nil)

		paths := options.Clearindex.Paths
		if len(paths) == 0 {
			paths = []string{"."}
		}

		this.vectorIndex.ClearPaths(this.ctx, paths)
		return nil

	case "showindex", "showindex <paths>":
		paths := options.Showindex.Paths
		this.initVectorIndex(paths)

		indexedPaths := this.vectorIndex.IndexedFiles()
		for _, path := range indexedPaths {
			this.Printf("%s\n", path)
		}

		return nil

	case "loadindex", "loadindex <paths>":
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

	case "index", "index <paths>":
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
		force := options.Index.Force

		err = this.vectorIndex.IndexPaths(this.ctx, paths, force)

		this.Printf("Done, %d files now loaded in the index\n", len(this.vectorIndex.IndexedFiles()))
		return err

	case "indexsearch <query>":
		this.initVectorIndex(nil)

		input := options.Indexsearch.Query
		if input == "" {
			return errors.New("Please provide search parameters")
		}
		numResults := options.Indexsearch.Results

		results, err := this.vectorIndex.Search(this.ctx, input, numResults)
		if err != nil {
			return err
		}

		for _, result := range results {
			this.StylePrintf(this.config.Styles.Highlight, "%s : %0.4f\n", result.FilePath, result.Score)
			this.Printf("%s\n", result.Content)
		}

	case "indexquestion <question>":
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

// Execute a command in a loop, if the exit status is non-zero then we call
// GPT to give us a fixed command and ask the user if they want to run it
func (this *ButterfishCtx) execAndCheck(ctx context.Context, cmd string) error {
	for {
		result, err := this.execCommand(cmd)
		// If the command succeeded, we're done
		if err == nil {
			return nil
		}

		this.ErrorPrintf("Command failed with status %d, requesting fix...\n", result.Status)

		prompt, err := this.promptLibrary.GetPrompt("fix_command",
			"command", cmd,
			"status", fmt.Sprintf("%d", result.Status),
			"output", string(result.LastOutput))
		if err != nil {
			return err
		}

		styleWriter := util.NewStyledWriter(this.out, this.config.Styles.Highlight)
		cacheWriter := util.NewCacheWriter(styleWriter)

		err = this.gptClient.CompletionStream(this.ctx, prompt, "", cacheWriter)
		if err != nil {
			return err
		}

		//this.StylePrintf(this.config.Styles.Highlight, "%s\n", resp)
		resp := string(cacheWriter.GetCache())

		// Find the last occurrence of '>' in the response and get the string
		// from there to the end
		lastGt := strings.LastIndex(resp, ">")
		if lastGt == -1 {
			return nil
		}

		cmd = strings.TrimSpace(resp[lastGt:])

		this.StylePrintf(this.config.Styles.Question, "Run this command? [y/N]: ")

		var input string
		_, err = fmt.Scanln(&input)
		if err != nil {
			return err
		}

		if strings.ToLower(input) != "y" {
			return nil
		}
	}
}

type executeResult struct {
	LastOutput []byte
	Status     int
}

// Function that executes a command on the local host as a child and streams
// the stdout/stderr to a writer. If the context is cancelled then the child
// process is killed.
// Returns an executeResult with status and last output if status != 0
func executeCommand(ctx context.Context, cmd string, out io.Writer) (*executeResult, error) {
	c := exec.CommandContext(ctx, "/bin/sh", "-c", cmd)
	cacheWriter := util.NewCacheWriter(out)
	c.Stdout = cacheWriter
	c.Stderr = cacheWriter

	err := c.Run()

	// check for a non-zero exit code
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			if status, ok := exitError.Sys().(syscall.WaitStatus); ok {
				status := status.ExitStatus()
				result := &executeResult{LastOutput: cacheWriter.GetLastN(512), Status: status}
				return result, err
			}
		}
	}

	return nil, err
}

// Execute the command as a child of this process (rather than a remote
// process), either from the command register or from a command string
func (this *ButterfishCtx) execCommand(cmd string) (*executeResult, error) {
	if cmd == "" && this.commandRegister == "" {
		return nil, errors.New("No command to execute")
	}
	if cmd == "" {
		cmd = this.commandRegister
	}

	if this.config.Verbose {
		this.StylePrintf(this.config.Styles.Question, "exec> %s\n", cmd)
	}
	return executeCommand(this.ctx, cmd, this.out)
}
