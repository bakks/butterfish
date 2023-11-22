package butterfish

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/lipgloss"
	"github.com/mitchellh/go-homedir"
	"github.com/sergi/go-diff/diffmatchpatch"
	"github.com/spf13/afero"
	"golang.org/x/term"

	"github.com/bakks/butterfish/prompt"
	"github.com/bakks/butterfish/util"
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

func (this *ButterfishCtx) ParseCommand(cmd string) (*kong.Context, *CliCommandConfig, error) {
	options := &CliCommandConfig{}
	parser, err := kong.New(options)
	if err != nil {
		return nil, nil, err
	}

	fields := strings.Fields(cmd)
	kongCtx, err := parser.Parse(fields)
	return kongCtx, options, err
}

// Kong CLI parser option configuration
type CliCommandConfig struct {
	Prompt struct {
		Prompt      []string `arg:"" help:"LLM model prompt, e.g. 'what is the unix shell?'" optional:""`
		SysMsg      string   `short:"s" default:"" help:"System message to send to model as instructions, e.g. 'respond succinctly'."`
		Model       string   `short:"m" default:"gpt-3.5-turbo" help:"LLM to use for the prompt."`
		NumTokens   int      `short:"n" default:"1024" help:"Maximum number of tokens to generate."`
		Temperature float32  `short:"T" default:"0.7" help:"Temperature to use for the prompt, higher temperature indicates more freedom/randomness when generating each token."`
		NoColor     bool     `default:"false" help:"Disable color output."`
		NoBackticks bool     `default:"false" help:"Stripe out backticks around codeblocks."`
	} `cmd:"" help:"Run an LLM prompt without wrapping, stream results back. This is a straight-through call to the LLM from the command line with a given prompt. This accepts piped input, if there is both piped input and a prompt then they will be concatenated together (prompt first). It is recommended that you wrap the prompt with quotes. The default GPT model is gpt-3.5-turbo."`

	Promptedit struct {
		File        string  `short:"f" default:"~/.config/butterfish/prompt.txt" help:"Cached prompt file to use." optional:""`
		Editor      string  `short:"e" default:"" help:"Editor to use for the prompt."`
		Model       string  `short:"m" default:"gpt-3.5-turbo" help:"GPT model to use for the prompt."`
		NumTokens   int     `short:"n" default:"1024" help:"Maximum number of tokens to generate."`
		Temperature float32 `short:"T" default:"0.7" help:"Temperature to use for the prompt, higher temperature indicates more freedom/randomness when generating each token."`
	} `cmd:"" help:"Like the prompt command, but this opens a local file with your default editor (set with the EDITOR env var) that will then be passed as a prompt in the LLM call."`

	Summarize struct {
		Files     []string `arg:"" help:"File paths to summarize." optional:""`
		ChunkSize int      `short:"c" default:"3600" help:"Number of bytes to summarize at a time if the file must be split up."`
		MaxChunks int      `short:"C" default:"8" help:"Maximum number of chunks to summarize from a specific file."`
	} `cmd:"" help:"Semantically summarize a list of files (or piped input). We read in the file, if it is short then we hand it directly to the LLM and ask for a summary. If it is longer then we break it into chunks and ask for a list of facts from each chunk (max 8 chunks), then concatenate facts and ask GPT for an overall summary."`

	Gencmd struct {
		Prompt []string `arg:"" help:"Prompt describing the desired shell command."`
		Force  bool     `short:"f" default:"false" help:"Execute the command without prompting."`
	} `cmd:"" help:"Generate a shell command from a prompt, i.e. pass in what you want, a shell command will be generated. Accepts piped input. You can use the -f command to execute it sight-unseen."`

	Exec struct {
		Command []string `arg:"" help:"Command to execute." optional:""`
	} `cmd:"" help:"Execute a command and try to debug problems. The command can either passed in or in the command register (if you have run gencmd in Console Mode)."`

	Index struct {
		Paths     []string `arg:"" help:"Paths to index." optional:""`
		Force     bool     `short:"f" default:"false" help:"Force re-indexing of files rather than skipping cached embeddings."`
		ChunkSize int      `short:"c" default:"512" help:"Number of bytes to embed at a time when the file is split up."`
		MaxChunks int      `short:"C" default:"256" help:"Maximum number of chunks to embed from a specific file."`
	} `cmd:"" help:"Recursively index the current directory using embeddings. This will read each file, split it into chunks, embed the chunks, and write a .butterfish_index file to each directory caching the embeddings. If you re-run this it will skip over previously embedded files unless you force a re-index. This implements an exponential backoff if you hit OpenAI API rate limits."`

	Clearindex struct {
		Paths []string `arg:"" help:"Paths to clear from the index." optional:""`
	} `cmd:"" help:"Clear paths from the index, both from the in-memory index (if in Console Mode) and to delete .butterfish_index files. Defaults to loading from the current directory but allows you to pass in paths to load."`

	Loadindex struct {
		Paths []string `arg:"" help:"Paths to load into the index." optional:""`
	} `cmd:"" help:"Load paths into the index. This is specifically for Console Mode when you want to load a set of cached indexes into memory. Defaults to loading from the current directory but allows you to pass in paths to load."`

	Showindex struct {
		Paths []string `arg:"" help:"Paths to show from the index." optional:""`
	} `cmd:"" help:"Show which files are present in the loaded index. You can pass in a path but it defaults to the current directory."`

	Indexsearch struct {
		Query   string `arg:"" help:"Query to search for."`
		Results int    `short:"r" default:"5" help:"Number of results to return."`
	} `cmd:"" help:"Search embedding index and return relevant file snippets. This uses the embedding API to embed the search string, then does a brute-force cosine similarity against every indexed chunk of text, returning those chunks and their scores."`

	Indexquestion struct {
		Question    string  `arg:"" help:"Question to ask."`
		Model       string  `short:"m" default:"gpt-3.5-turbo" help:"GPT model to use for the prompt."`
		NumTokens   int     `short:"n" default:"1024" help:"Maximum number of tokens to generate."`
		Temperature float32 `short:"T" default:"0.7" help:"Temperature to use for the prompt."`
	} `cmd:"" help:"Ask a question using the embeddings index. This fetches text snippets from the index and passes them to the LLM to generate an answer, thus you need to run the index command first."`
}

func (this *ButterfishCtx) getPipedStdin() string {
	if !this.InConsoleMode && util.IsPipedStdin() {
		stdin, err := io.ReadAll(os.Stdin)
		if err != nil {
			return ""
		}
		return string(stdin)
	}
	return ""
}

func (this *ButterfishCtx) getPipedStdinReader() io.Reader {
	if !this.InConsoleMode && util.IsPipedStdin() {
		return os.Stdin
	}
	return nil
}

// Given a parsed input split into a slice, join the string together
// and remove any leading/trailing quotes
func (this *ButterfishCtx) cleanInput(input []string) string {
	// If we're not in console mode and we have piped data then use that as input
	if !this.InConsoleMode && util.IsPipedStdin() {
		stdin, err := io.ReadAll(os.Stdin)
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
func (this *ButterfishCtx) ExecCommand(parsed *kong.Context, options *CliCommandConfig) error {

	switch parsed.Command() {
	case "exit", "quit":
		fmt.Fprintf(this.Out, "Exiting...")
		this.Cancel()
		return nil

	case "help":
		parsed.Kong.Stdout = this.Out
		parsed.PrintUsage(false)

	case "prompt", "prompt <prompt>":
		// The prompt command accepts both stdin and a prompt string, but needs at
		// least one of them. If we have both then we concatenate them with prompt
		// first.
		promptArr := options.Prompt.Prompt
		prompt := ""
		if promptArr != nil && len(promptArr) > 0 {
			prompt = strings.Join(promptArr, " ")
		}
		piped := this.getPipedStdin()

		var input string

		if piped == "" && prompt == "" {
			return errors.New("Please provide a prompt")
		} else if piped == "" {
			input = prompt
		} else if prompt == "" {
			input = piped
		} else {
			input = fmt.Sprintf("%s\n%s", prompt, piped)
		}

		return this.Prompt(
			input,
			options.Prompt.SysMsg,
			options.Prompt.Model,
			options.Prompt.NumTokens,
			options.Prompt.Temperature,
			options.Prompt.NoColor,
			options.Prompt.NoBackticks,
			this.Config.Verbose)

	case "promptedit":
		targetFile := options.Promptedit.File
		editor := options.Promptedit.Editor

		targetFile, err := homedir.Expand(targetFile)
		if err != nil {
			return err
		}

		// get EDITOR env var if not specified
		if editor == "" {
			editor = os.Getenv("EDITOR")
		}
		if editor == "" {
			editor = "vi"
			if this.Config.Verbose > 0 {
				this.StylePrintf(this.Config.Styles.Grey, "Defaulting to %s for editor, you can set this with --editor or the EDITOR env var\n", editor)
			}
		}

		if this.Config.Verbose > 0 {
			this.StylePrintf(this.Config.Styles.Grey, "%s %s\n", editor, targetFile)
		}

		cmd := exec.Command(editor, targetFile)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = os.Environ()

		err = cmd.Run()
		if err != nil {
			return err
		}

		content, err := os.ReadFile(targetFile)

		if this.Config.Verbose > 0 {
			this.StylePrintf(this.Config.Styles.Question, "%s\n", string(content))
		}

		return this.Prompt(
			string(content),
			"",
			options.Prompt.Model,
			options.Prompt.NumTokens,
			options.Prompt.Temperature,
			false,
			false,
			0)

	case "summarize":
		chunks, err := util.GetChunks(
			os.Stdin,
			options.Summarize.ChunkSize,
			options.Summarize.MaxChunks)

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

		err := this.SummarizePaths(files,
			options.Summarize.ChunkSize,
			options.Summarize.MaxChunks)
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

		// trim whitespace
		cmd = strings.TrimSpace(cmd)

		if !options.Gencmd.Force {
			this.StylePrintf(this.Config.Styles.Highlight, "%s\n", cmd)
		} else {
			_, err := this.execCommand(cmd)
			if err != nil {
				return err
			}
		}
		return nil

	case "exec", "exec <command>":
		input := this.cleanInput(options.Exec.Command)
		if input == "" {
			input = this.CommandRegister
		}

		if input == "" {
			return errors.New("No command to execute")
		}

		return this.execAndCheck(this.Ctx, input)

	case "clearindex", "clearindex <paths>":
		this.initVectorIndex(nil)

		paths := options.Clearindex.Paths
		if len(paths) == 0 {
			paths = []string{"."}
		}

		this.VectorIndex.ClearPaths(this.Ctx, paths)
		return nil

	case "showindex", "showindex <paths>":
		paths := options.Showindex.Paths
		this.initVectorIndex(paths)

		indexedPaths := this.VectorIndex.IndexedFiles()
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

		err := this.VectorIndex.LoadPaths(this.Ctx, paths)
		if err != nil {
			return err
		}
		this.Printf("Loaded %d files\n", len(this.VectorIndex.IndexedFiles()))

	case "index", "index <paths>":
		paths := options.Index.Paths
		if len(paths) == 0 {
			paths = []string{"."}
		}

		this.Printf("Indexing %s\n", strings.Join(paths, ", "))
		this.initVectorIndex(paths)

		err := this.VectorIndex.LoadPaths(this.Ctx, paths)
		if err != nil {
			return err
		}
		force := options.Index.Force

		err = this.VectorIndex.IndexPaths(
			this.Ctx,
			paths,
			force,
			options.Index.ChunkSize,
			options.Index.MaxChunks)
		if err != nil {
			return err
		}

		this.Printf("Done, %d files now loaded in the index\n", len(this.VectorIndex.IndexedFiles()))
		return nil

	case "indexsearch <query>":
		this.initVectorIndex(nil)

		input := options.Indexsearch.Query
		if input == "" {
			return errors.New("Please provide search parameters")
		}
		numResults := options.Indexsearch.Results

		results, err := this.VectorIndex.Search(this.Ctx, input, numResults)
		if err != nil {
			return err
		}

		for _, result := range results {
			this.StylePrintf(this.Config.Styles.Highlight, "%s : %0.4f\n", result.FilePath, result.Score)
			this.Printf("%s\n", result.Content)
		}

	case "indexquestion <question>":
		this.initVectorIndex(nil)
		input := options.Indexquestion.Question

		if input == "" {
			return errors.New("Please provide a question")
		}
		if this.VectorIndex == nil {
			return errors.New("No vector index loaded")
		}

		results, err := this.VectorIndex.Search(this.Ctx, input, 3)
		if err != nil {
			return err
		}
		samples := []string{}

		for _, result := range results {
			samples = append(samples, result.Content)
		}

		exerpts := strings.Join(samples, "\n---\n")

		prompt, err := this.PromptLibrary.GetPrompt(prompt.PromptQuestion,
			"snippets", exerpts,
			"question", input)
		if err != nil {
			return err
		}

		req := &util.CompletionRequest{
			Ctx:           this.Ctx,
			Prompt:        prompt,
			Model:         options.Indexquestion.Model,
			MaxTokens:     options.Indexquestion.NumTokens,
			Temperature:   options.Indexquestion.Temperature,
			SystemMessage: "N/A",
		}

		_, err = this.LLMClient.CompletionStream(req, this.Out)
		return err

	default:
		return errors.New("Unrecognized command: " + parsed.Command())

	}

	return nil
}

func styleToEscape(color lipgloss.TerminalColor) string {
	r, g, b, _ := color.RGBA()
	color256 := 16 + (36 * (r / 257 / 51)) + (6 * (g / 257 / 51)) + (b / 257 / 51)
	return fmt.Sprintf("\x1b[38;5;%dm", color256)
}

func (this *ButterfishCtx) Prompt(
	promptStr string, // prompt or last user message in chatgpt completion
	sysMsg string, // sysmsg, or background instructions
	model string, // model to use
	maxTokens int, // max tokens of response
	temperature float32,
	noColor bool,
	noBackticks bool,
	verbose int,
) error {

	var writer io.Writer
	if !noColor {
		color := styleToEscape(this.Config.Styles.Answer.GetForeground())
		this.Out.Write([]byte(color))

		termWidth, _, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil && this.Config.Verbose > 0 {
			this.ErrorPrintf("Could not get terminal width, defaulting to 40 for style writer\n")
			termWidth = 40
		}

		writer = util.NewStyleCodeblocksWriter(this.Out, termWidth, color)
	} else if noBackticks {
		writer = util.NewStripbackticksWriter(this.Out)
	} else {
		writer = this.Out
	}

	if sysMsg == "" {
		var err error
		sysMsg, err = this.PromptLibrary.GetPrompt(prompt.PromptSystemMessage)
		if err != nil {
			return err
		}
	}

	//util.InitLogging(context.Background())

	req := &util.CompletionRequest{
		Ctx:           this.Ctx,
		Prompt:        promptStr,
		Model:         model,
		MaxTokens:     maxTokens,
		Temperature:   temperature,
		SystemMessage: sysMsg,
		Verbose:       verbose > 0,
	}

	_, err := this.LLMClient.CompletionStream(req, writer)
	return err
}

func (this *ButterfishCtx) diffStrings(a, b string) string {
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(a, b, false)

	strBuilder := strings.Builder{}
	for _, diff := range diffs {
		switch diff.Type {
		case diffmatchpatch.DiffInsert:
			strBuilder.WriteString(
				this.StyleSprintf(this.Config.Styles.Go, "%s", diff.Text))
		case diffmatchpatch.DiffEqual:
			strBuilder.WriteString(
				this.StyleSprintf(this.Config.Styles.Foreground, "%s", diff.Text))
		}
	}

	return strBuilder.String()
}

// Given a description of functionality, we call GPT to generate a shell
// command
func (this *ButterfishCtx) gencmdCommand(description string) (string, error) {
	promptStr, err := this.PromptLibrary.GetPrompt("generate_command", "content", description)
	if err != nil {
		return "", err
	}

	sysMsg, err := this.PromptLibrary.GetPrompt(prompt.PromptSystemMessage)
	if err != nil {
		return "", err
	}
	req := &util.CompletionRequest{
		Ctx:           this.Ctx,
		Prompt:        promptStr,
		Model:         this.Config.GencmdModel,
		MaxTokens:     this.Config.GencmdMaxTokens,
		Temperature:   this.Config.GencmdTemperature,
		SystemMessage: sysMsg,
	}

	resp, err := this.LLMClient.Completion(req)
	if err != nil {
		return "", err
	}

	this.updateCommandRegister(resp.Completion)
	return resp.Completion, nil
}

// We're parsing the results from an LLM requesting a command fix, we expect
// that there will be natural language text in the string and the command
// will appear somewhere like:
// > command
// or like
// ```
// command
// ```
// If there are multiple commands we just take the first one.
func fixCommandParse(s string) (string, error) {
	// regex for the > pattern
	re1 := regexp.MustCompile(`\n> (.*)`)
	matches := re1.FindStringSubmatch(s)
	if len(matches) == 2 {
		return strings.TrimSpace(matches[1]), nil
	}

	// regex for the ``` pattern
	re2 := regexp.MustCompile("```\n(.*)\n```")
	matches = re2.FindStringSubmatch(s)
	if len(matches) == 2 {
		return strings.TrimSpace(matches[1]), nil
	}

	return "", errors.New("Could not find command in response")
}

// Execute a command in a loop, if the exit status is non-zero then we call
// GPT to give us a fixed command and ask the user if they want to run it
func (this *ButterfishCtx) execAndCheck(ctx context.Context, cmd string) error {
	for {
		result, err := this.execCommand(cmd)
		if err != nil {
			return err
		}
		// If the command succeeded, we're done
		if result.Status == 0 {
			return nil
		}

		this.ErrorPrintf("Command failed with status %d, requesting fix...\n", result.Status)

		prompt, err := this.PromptLibrary.GetPrompt("fix_command",
			"command", cmd,
			"status", fmt.Sprintf("%d", result.Status),
			"output", string(result.LastOutput))
		if err != nil {
			return err
		}

		styleWriter := util.NewStyledWriter(this.Out, this.Config.Styles.Highlight)

		req := &util.CompletionRequest{
			Ctx:           this.Ctx,
			Prompt:        prompt,
			Model:         this.Config.ExeccheckModel,
			MaxTokens:     this.Config.ExeccheckMaxTokens,
			Temperature:   this.Config.ExeccheckTemperature,
			SystemMessage: "N/A",
		}

		response, err := this.LLMClient.CompletionStream(req, styleWriter)
		if err != nil {
			return err
		}

		cmd, err = fixCommandParse(response.Completion)
		if err != nil {
			return err
		}

		this.StylePrintf(this.Config.Styles.Question, "Run this command? [y/N]: ")

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
// Returns an executeResult with status and last output
func executeCommand(ctx context.Context, cmd string, out io.Writer) (*executeResult, error) {
	c := exec.CommandContext(ctx, "/bin/sh", "-c", cmd)
	cacheWriter := util.NewCacheWriter(out)
	c.Stdout = cacheWriter
	c.Stderr = cacheWriter

	err := c.Run()

	result := &executeResult{LastOutput: cacheWriter.GetCache(), Status: 0}

	// check for a non-zero exit code
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			if status, ok := exitError.Sys().(syscall.WaitStatus); ok {
				result.Status = status.ExitStatus()
				// this is OK in this context so set err to nil
				return result, nil
			}
		}
	}

	return result, err
}

// Execute the command as a child of this process (rather than a remote
// process), either from the command register or from a command string
func (this *ButterfishCtx) execCommand(cmd string) (*executeResult, error) {
	if cmd == "" && this.CommandRegister == "" {
		return nil, errors.New("No command to execute")
	}
	if cmd == "" {
		cmd = this.CommandRegister
	}

	if this.Config.Verbose > 0 {
		this.StylePrintf(this.Config.Styles.Question, "exec> %s\n", cmd)
	}
	return executeCommand(this.Ctx, cmd, this.Out)
}

// Iterate through a list of file paths and summarize each
func (this *ButterfishCtx) SummarizePaths(paths []string, chunkSize, maxChunks int) error {
	for _, path := range paths {
		err := this.SummarizePath(path, chunkSize, maxChunks)
		if err != nil {
			return err
		}
	}

	return nil
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
func (this *ButterfishCtx) SummarizePath(path string, chunkSize, maxChunks int) error {
	this.StylePrintf(this.Config.Styles.Question, "Summarizing %s\n", path)

	fs := afero.NewOsFs()
	chunks, err := util.GetFileChunks(this.Ctx, fs, path, chunkSize, maxChunks)
	if err != nil {
		return err
	}

	return this.SummarizeChunks(chunks)
}

func (this *ButterfishCtx) updateCommandRegister(cmd string) {
	// If we're not in console mode then we don't care about updating the register
	if !this.InConsoleMode {
		return
	}

	cmd = strings.TrimSpace(cmd)
	this.CommandRegister = cmd
	this.Printf("Command register updated to:\n")
	this.StylePrintf(this.Config.Styles.Answer, "%s\n", cmd)
	this.Printf("Run exec or execremote to execute\n")
}

func (this *ButterfishCtx) SummarizeChunks(chunks [][]byte) error {
	writer := util.NewStyledWriter(this.Out, this.Config.Styles.Foreground)
	req := &util.CompletionRequest{
		Ctx:           this.Ctx,
		Model:         this.Config.SummarizeModel,
		MaxTokens:     this.Config.SummarizeMaxTokens,
		Temperature:   this.Config.SummarizeTemperature,
		SystemMessage: "N/A",
	}

	if len(chunks) == 1 {
		// the entire document fits within the token limit, summarize directly
		prompt, err := this.PromptLibrary.GetPrompt(prompt.PromptSummarize,
			"content", string(chunks[0]))
		if err != nil {
			return err
		}
		req.Prompt = prompt

		_, err = this.LLMClient.CompletionStream(req, writer)
	}

	// the document doesn't fit within the token limit, we'll iterate over it
	// and summarize each chunk as facts, then ask for a summary of facts
	facts := strings.Builder{}

	for _, chunk := range chunks {
		if len(chunk) < 16 { // if we have a tiny chunk, skip it
			break
		}

		prompt, err := this.PromptLibrary.GetPrompt(prompt.PromptSummarizeFacts,
			"content", string(chunk))
		if err != nil {
			return err
		}
		req.Prompt = prompt
		resp, err := this.LLMClient.Completion(req)
		if err != nil {
			return err
		}
		facts.WriteString(resp.Completion)
		facts.WriteString("\n")
	}

	mergedFacts := facts.String()
	prompt, err := this.PromptLibrary.GetPrompt(prompt.PromptSummarizeListOfFacts,
		"content", mergedFacts)
	if err != nil {
		return err
	}

	req.Prompt = prompt
	_, err = this.LLMClient.CompletionStream(req, writer)
	return err
}
