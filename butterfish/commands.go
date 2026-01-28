package butterfish

import (
	"context"
	"encoding/json"
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
		Prompt        []string `arg:"" help:"LLM model prompt, e.g. 'what is the unix shell?'" optional:""`
		SystemMessage string   `short:"s" default:"" help:"System message to send to model as instructions, e.g. 'respond succinctly'."`
		Model         string   `short:"m" default:"gpt-4-turbo" help:"LLM to use for the prompt."`
		NumTokens     int      `short:"n" default:"1024" help:"Maximum number of tokens to generate."`
		Temperature   float32  `short:"T" default:"0.7" help:"Temperature to use for the prompt, higher temperature indicates more freedom/randomness when generating each token."`
		Functions     string   `short:"f" default:"" help:"Path to json file with functions to use for prompt."`
		NoColor       bool     `default:"false" help:"Disable color output."`
		NoBackticks   bool     `default:"false" help:"Strip out backticks around codeblocks."`
	} `cmd:"" help:"Run an LLM prompt without wrapping, stream results back. This is a straight-through call to the LLM from the command line with a given prompt. This accepts piped input, if there is both piped input and a prompt then they will be concatenated together (prompt first). It is recommended that you wrap the prompt with quotes. The default GPT model is gpt-4-turbo."`

	Promptedit struct {
		File        string  `short:"f" default:"~/.config/butterfish/prompt.txt" help:"Cached prompt file to use." optional:""`
		Editor      string  `short:"e" default:"" help:"Editor to use for the prompt."`
		Model       string  `short:"m" default:"gpt-4-turbo" help:"GPT model to use for the prompt."`
		NumTokens   int     `short:"n" default:"1024" help:"Maximum number of tokens to generate."`
		Temperature float32 `short:"T" default:"0.7" help:"Temperature to use for the prompt, higher temperature indicates more freedom/randomness when generating each token."`
	} `cmd:"" help:"Like the prompt command, but this opens a local file with your default editor (set with the EDITOR env var) that will then be passed as a prompt in the LLM call."`

	Gencmd struct {
		Prompt []string `arg:"" help:"Prompt describing the desired shell command."`
		Force  bool     `short:"f" default:"false" help:"Execute the command without prompting."`
	} `cmd:"" help:"Generate a shell command from a prompt, i.e. pass in what you want, a shell command will be generated. Accepts piped input. You can use the -f command to execute it sight-unseen."`

	Exec struct {
		Command []string `arg:"" help:"Command to execute." optional:""`
	} `cmd:"" help:"Execute a command and try to debug problems. The command can either passed in or in the command register (if you have run gencmd in Console Mode)."`
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

// Manage a buffer of lines, we want to be able to replace a range of lines
// A function to handle a cmd string when received from consoleCommand channel
func (this *ButterfishCtx) ExecCommand(
	parsed *kong.Context,
	options *CliCommandConfig,
) error {

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

		commandConfig := &promptCommand{
			Prompt:      input,
			SysMsg:      options.Prompt.SystemMessage,
			Model:       options.Prompt.Model,
			NumTokens:   options.Prompt.NumTokens,
			Temperature: options.Prompt.Temperature,
			Functions:   options.Prompt.Functions,
			NoColor:     options.Prompt.NoColor,
			NoBackticks: options.Prompt.NoBackticks,
			Verbose:     this.Config.Verbose,
		}

		_, err := this.Prompt(commandConfig)
		return err

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
		if err != nil {
			return err
		}

		if this.Config.Verbose > 0 {
			this.StylePrintf(this.Config.Styles.Question, "%s\n", string(content))
		}

		commandConfig := &promptCommand{
			Prompt:      string(content),
			Model:       options.Promptedit.Model,
			NumTokens:   options.Promptedit.NumTokens,
			Temperature: options.Promptedit.Temperature,
			Verbose:     this.Config.Verbose,
		}

		_, err = this.Prompt(commandConfig)
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

type promptCommand struct {
	Prompt      string
	SysMsg      string
	Model       string
	NumTokens   int
	Temperature float32
	Functions   string
	NoColor     bool
	NoBackticks bool
	Verbose     int
	History     []util.HistoryBlock
	Tools       []util.ToolDefinition
}

func (this *ButterfishCtx) Prompt(cmd *promptCommand) (*util.CompletionResponse, error) {
	writer := this.Out

	if !cmd.NoColor {
		color := styleToEscape(this.Config.Styles.Answer.GetForeground())
		highlight := styleToEscape(this.Config.Styles.Highlight.GetForeground())
		this.Out.Write([]byte(color))

		termWidth, _, _ := term.GetSize(int(os.Stdout.Fd()))

		if termWidth > 0 {
			colorScheme := "monokai"
			if !this.Config.ColorDark {
				colorScheme = "monokailight"
			}
			writer = util.NewStyleCodeblocksWriter(this.Out, termWidth, color, highlight, colorScheme)
		}
	} else if cmd.NoBackticks {
		// this is an else because the code blocks writer will strip out backticks
		// on its own, so this is only used if we don't have color AND we don't
		// want backticks
		writer = util.NewStripbackticksWriter(this.Out)
	}

	sysMsg := cmd.SysMsg
	if sysMsg == "" {
		var err error
		sysMsg, err = this.PromptLibrary.GetPrompt(prompt.PromptSystemMessage)
		if err != nil {
			return nil, err
		}
	}

	var functions []util.FunctionDefinition

	// if we have a functions file, load it and parse it
	if cmd.Functions != "" {
		// read raw file
		functionsJson, err := os.ReadFile(cmd.Functions)
		if err != nil {
			return nil, err
		}

		// parse json
		err = json.Unmarshal(functionsJson, &functions)
		if err != nil {
			return nil, err
		}
	}

	req := &util.CompletionRequest{
		Ctx:           this.Ctx,
		Prompt:        cmd.Prompt,
		Model:         cmd.Model,
		MaxTokens:     cmd.NumTokens,
		Temperature:   cmd.Temperature,
		SystemMessage: sysMsg,
		Verbose:       cmd.Verbose > 0,
		Functions:     functions,
		Tools:         cmd.Tools,
		HistoryBlocks: cmd.History,
		TokenTimeout:  this.Config.TokenTimeout,
	}

	return this.LLMClient.CompletionStream(req, writer)
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
		TokenTimeout:  this.Config.TokenTimeout,
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
			TokenTimeout:  this.Config.TokenTimeout,
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
