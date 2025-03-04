package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/joho/godotenv"
	"github.com/mitchellh/go-homedir"

	//_ "net/http/pprof"

	bf "github.com/xuzhougeng/butterfish/butterfish"
	"github.com/xuzhougeng/butterfish/util"
)

var ( // these are filled in at build time
	BuildVersion   string
	BuildCommit    string
	BuildTimestamp string
)

const description = `Do useful things with LLMs from the command line, with a bent towards software engineering.

Butterfish is a command line tool for working with LLMs. It has two modes: CLI command mode, used to prompt LLMs, summarize files, and manage embeddings, and Shell mode: Wraps your local shell to provide easy prompting and autocomplete.

Butterfish looks for an API key in OPENAI_API_KEY, or alternatively stores an OpenAI auth token at ~/.config/butterfish/butterfish.env.

Prompts are stored in ~/.config/butterfish/prompts.yaml. Butterfish logs to ~/.butterfish/logs/butterfish.log. To print the full prompts and responses from the OpenAI API, use the --verbose flag. Support can be found at https://github.com/xuzhougeng/butterfish.

If you do not have OpenAI free credits then you will need a subscription and you will need to pay for OpenAI API use. If you're using Shell Mode, autosuggest will probably be the most expensive part. You can reduce spend by disabling shell autosuggest (-A) or increasing the autosuggest timeout (e.g. -t 2000). See "butterfish shell --help".
`
const license = "MIT License - Copyright (c) 2023 Peter Bakkum"
const defaultEnvPath = "~/.config/butterfish/butterfish.env"
const defaultPromptPath = "~/.config/butterfish/prompts.yaml"

const shell_help = `Start the Butterfish shell wrapper. This wraps your existing shell, giving you access to LLM prompting by starting your command with a capital letter. LLM calls include prior shell context. This is great for keeping a chat-like terminal open, sending written prompts, debugging commands, and iterating on past actions.

Use:
  - Type a normal command, like 'ls -l' and press enter to execute it
  - Start a command with a capital letter to send it to GPT, like 'How do I recursively find local .py files?'
  - Autosuggest will print command completions, press tab to fill them in
  - GPT will be able to see your shell history, so you can ask contextual questions like 'why didnt my last command work?'
	- Start a command with ! to enter Goal Mode, in which GPT will act as an Agent attempting to accomplish your goal by executing commands, for example '!Run make in this directory and debug any problems'.
	- Start a command with !! to enter Unsafe Goal Mode, in which GPT will execute commands without confirmation. USE WITH CAUTION.

Here are special Butterfish commands:
  - Help : Give hints about usage.
  - Status : Show the current Butterfish configuration.
  - History : Print out the history that would be sent in a GPT prompt.

If you do not have OpenAI free credits then you will need a subscription and you will need to pay for OpenAI API use. Autosuggest will probably be the most expensive feature. You can reduce spend by disabling shell autosuggest (-A) or increasing the autosuggest timeout (e.g. -t 2000).`

type VerboseFlag bool

var verboseCount int

// This is a hook to count how many times the verbose flag is set, e.g. -vvv,
// but apparently it's always called at least once even if no flag is set
func (v *VerboseFlag) BeforeResolve() error {
	verboseCount++
	return nil
}

// Kong configuration for shell arguments (shell meaning when butterfish is
// invoked, rather than when we're inside a butterfish console).
// Kong will parse os.Args based on this struct.
type CliConfig struct {
	Verbose      VerboseFlag      `short:"v" default:"false" help:"Verbose mode, prints full LLM prompts (sometimes to log file). Use multiple times for more verbosity, e.g. -vv."`
	Log          bool             `short:"L" default:"false" help:"Write verbose content to a log file rather than stdout, usually ~/.butterfish/logs/butterfish.log"`
	Version      kong.VersionFlag `short:"V" help:"Print version information and exit."`
	BaseURL      string           `short:"u" default:"https://api.openai.com/v1" help:"Base URL for OpenAI-compatible API. Enables local models with a compatible interface."`
	TokenTimeout int              `short:"z" default:"10000" help:"Timeout before first prompt token is received and between individual tokens. In milliseconds."`
	LightColor   bool             `short:"l" default:"false" help:"Light color mode, appropriate for a terminal with a white(ish) background"`

	Shell struct {
		Bin                        string  `short:"b" default:"" help:"Shell binary to use, defaults to $SHELL."`
		Model                      string  `short:"m" default:"" help:"LLM to use for shell prompts."`
		AutosuggestModel           string  `short:"a" default:"" help:"LLM to use for shell autosuggestions."`
		AutosuggestDisabled        bool    `short:"A" default:"false" help:"Disable shell autosuggestions."`
		AutosuggestTimeout         int     `short:"t" default:"1000" help:"Timeout for shell autosuggestions in milliseconds."`
		NewlineAutosuggestTimeout  int     `short:"T" default:"2000" help:"Timeout for shell autosuggestions after newline in milliseconds."`
		NoCommandPrompt            bool    `short:"P" default:"false" help:"Don't modify the command prompt."`
		MaxPromptTokens            int     `short:"p" default:"4096" help:"Maximum number of tokens to use for shell prompts."`
		MaxHistoryBlockTokens      int     `short:"H" default:"2048" help:"Maximum number of tokens to use for shell history blocks."`
		MaxResponseTokens          int     `short:"r" default:"1024" help:"Maximum number of tokens to generate for shell responses."`
	} `cmd:"shell" help:"${shell_help}"`

	Completion struct {
		Shell string `arg:"" required:"" enum:"bash,zsh,fish" help:"Shell to generate completion script for (bash, zsh, fish)"`
	} `cmd:"completion" help:"Generate shell completion script"`

	bf.CliCommandConfig
}

const bashCompletion = `
_butterfish_completion() {
    local cur prev opts
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"
    opts="shell prompt promptedit edit summarize gencmd exec index clearindex loadindex showindex indexsearch indexquestion image completion"

    case "${prev}" in
        butterfish)
            COMPREPLY=( $(compgen -W "${opts}" -- ${cur}) )
            return 0
            ;;
        completion)
            COMPREPLY=( $(compgen -W "bash zsh fish" -- ${cur}) )
            return 0
            ;;
        *)
            COMPREPLY=()
            return 0
            ;;
    esac
}

complete -F _butterfish_completion butterfish
`

const zshCompletion = `#compdef butterfish

_butterfish() {
    local -a commands
    commands=(
        'shell:Start the Butterfish shell wrapper'
        'prompt:Run an LLM prompt'
        'promptedit:Edit and run a prompt'
        'edit:Edit a file using LLM'
        'summarize:Summarize files'
        'gencmd:Generate shell commands'
        'exec:Execute and debug commands'
        'index:Index files for search'
        'clearindex:Clear index'
        'loadindex:Load index'
        'showindex:Show indexed files'
        'indexsearch:Search in indexed files'
        'indexquestion:Ask questions about indexed files'
        'image:Analyze images'
        'completion:Generate shell completion script'
    )

    _arguments -C \
        '1: :->cmds' \
        '*:: :->args'

    case "$state" in
        cmds)
            _describe -t commands 'butterfish commands' commands
            ;;
        args)
            case $words[1] in
                completion)
                    _values 'shell' 'bash' 'zsh' 'fish'
                    ;;
            esac
            ;;
    esac
}

_butterfish
`

const fishCompletion = `
function __fish_butterfish_no_subcommand
    set cmd (commandline -opc)
    if [ (count $cmd) -eq 1 ]
        return 0
    end
    return 1
end

complete -c butterfish -n '__fish_butterfish_no_subcommand' -a shell -d 'Start the Butterfish shell wrapper'
complete -c butterfish -n '__fish_butterfish_no_subcommand' -a prompt -d 'Run an LLM prompt'
complete -c butterfish -n '__fish_butterfish_no_subcommand' -a promptedit -d 'Edit and run a prompt'
complete -c butterfish -n '__fish_butterfish_no_subcommand' -a edit -d 'Edit a file using LLM'
complete -c butterfish -n '__fish_butterfish_no_subcommand' -a summarize -d 'Summarize files'
complete -c butterfish -n '__fish_butterfish_no_subcommand' -a gencmd -d 'Generate shell commands'
complete -c butterfish -n '__fish_butterfish_no_subcommand' -a exec -d 'Execute and debug commands'
complete -c butterfish -n '__fish_butterfish_no_subcommand' -a index -d 'Index files for search'
complete -c butterfish -n '__fish_butterfish_no_subcommand' -a clearindex -d 'Clear index'
complete -c butterfish -n '__fish_butterfish_no_subcommand' -a loadindex -d 'Load index'
complete -c butterfish -n '__fish_butterfish_no_subcommand' -a showindex -d 'Show indexed files'
complete -c butterfish -n '__fish_butterfish_no_subcommand' -a indexsearch -d 'Search in indexed files'
complete -c butterfish -n '__fish_butterfish_no_subcommand' -a indexquestion -d 'Ask questions about indexed files'
complete -c butterfish -n '__fish_butterfish_no_subcommand' -a image -d 'Analyze images'
complete -c butterfish -n '__fish_butterfish_no_subcommand' -a completion -d 'Generate shell completion script'

complete -c butterfish -n '__fish_seen_subcommand_from completion' -a "bash zsh fish" -d 'Shell type'
`

func getModelType(model string) bf.ModelType {
	model = strings.ToLower(model)
	switch {
	case strings.Contains(model, "gpt") || strings.HasPrefix(model, "openai/"):
		return bf.ModelTypeOpenAI
	case strings.Contains(model, "claude") || strings.HasPrefix(model, "anthropic/"):
		return bf.ModelTypeAnthropic
	case strings.Contains(model, "gemini") || strings.HasPrefix(model, "google/"):
		return bf.ModelTypeGemini
	case strings.Contains(model, "llama"):
		return bf.ModelTypeLlama
	case strings.Contains(model, "mistral"):
		return bf.ModelTypeMistral
	default:
		return bf.ModelTypeUnknown
	}
}

func getOpenAIToken() string {
	path, err := homedir.Expand(defaultEnvPath)
	if err != nil {
		log.Fatal(err)
	}

	// We attempt to get a token from env vars plus an env file
	godotenv.Load(path)

	token := os.Getenv("OPENAI_TOKEN")
	if token != "" {
		return token
	}

	token = os.Getenv("OPENAI_API_KEY")
	if token != "" {
		return token
	}

	return ""
}

func isAnthropicModel(model string) bool {
	model = strings.ToLower(model)
	return strings.Contains(model, "claude") || strings.HasPrefix(model, "anthropic/")
}

func loadModelConfig(config *bf.ButterfishConfig) {
	// Load model configs from env if present
	if model := os.Getenv("BUTTERFISH_PROMPT_MODEL"); model != "" {
		config.ShellPromptModel = model
		config.ModelType = getModelType(model)
	}
	if model := os.Getenv("BUTTERFISH_AUTOSUGGEST_MODEL"); model != "" {
		config.ShellAutosuggestModel = model
		if config.ModelType == bf.ModelTypeUnknown {
			config.ModelType = getModelType(model)
		}
	}
	if model := os.Getenv("BUTTERFISH_GENCMD_MODEL"); model != "" {
		config.GencmdModel = model
		if config.ModelType == bf.ModelTypeUnknown {
			config.ModelType = getModelType(model)
		}
	}
	if model := os.Getenv("BUTTERFISH_EXECCHECK_MODEL"); model != "" {
		config.ExeccheckModel = model
		if config.ModelType == bf.ModelTypeUnknown {
			config.ModelType = getModelType(model)
		}
	}
	if model := os.Getenv("BUTTERFISH_SUMMARIZE_MODEL"); model != "" {
		config.SummarizeModel = model
		if config.ModelType == bf.ModelTypeUnknown {
			config.ModelType = getModelType(model)
		}
	}
	if model := os.Getenv("BUTTERFISH_IMAGE_MODEL"); model != "" {
		config.ImageModel = model
		if config.ModelType == bf.ModelTypeUnknown {
			config.ModelType = getModelType(model)
		}
	}
}

func makeButterfishConfig(options *CliConfig) *bf.ButterfishConfig {
	config := bf.MakeButterfishConfig()
	config.OpenAIToken = getOpenAIToken()
	
	// Check env for BASE_URL first
	if baseURL := os.Getenv("BUTTERFISH_BASE_URL"); baseURL != "" {
		config.BaseURL = baseURL
	} else {
		config.BaseURL = options.BaseURL
	}
	
	// Load model configs from env first
	loadModelConfig(config)
	
	// Set default models if not specified in env
	if config.ShellPromptModel == "" {
		config.ShellPromptModel = "gpt-3.5-turbo"
		config.ModelType = bf.ModelTypeOpenAI
	}
	if config.ShellAutosuggestModel == "" {
		config.ShellAutosuggestModel = "gpt-3.5-turbo-instruct"
	}
	if config.GencmdModel == "" {
		config.GencmdModel = "gpt-3.5-turbo"
	}
	if config.ExeccheckModel == "" {
		config.ExeccheckModel = "gpt-3.5-turbo"
	}
	if config.SummarizeModel == "" {
		config.SummarizeModel = "gpt-3.5-turbo"
	}

	// Set model-specific configurations
	switch config.ModelType {
	case bf.ModelTypeAnthropic:
		config.DefaultSystemMessage = "You are Claude, an AI assistant. You help users with their tasks in a clear and concise way."
	case bf.ModelTypeGemini:
		config.DefaultSystemMessage = "You are Gemini, an AI assistant. You help users with their tasks in a clear and concise way."
	case bf.ModelTypeLlama:
		config.DefaultSystemMessage = "You are a helpful AI assistant. You help users with their tasks in a clear and concise way."
	case bf.ModelTypeMistral:
		config.DefaultSystemMessage = "You are a helpful AI assistant. You help users with their tasks in a clear and concise way."
	}
	
	config.PromptLibraryPath = defaultPromptPath
	config.TokenTimeout = time.Duration(options.TokenTimeout) * time.Millisecond

	if options.Verbose {
		config.Verbose = verboseCount
	}

	return config
}

func getBuildInfo() string {
	buildOs := runtime.GOOS
	buildArch := runtime.GOARCH
	return fmt.Sprintf("%s %s %s\n(commit %s) (built %s)\n%s\n", BuildVersion, buildOs, buildArch, BuildCommit, BuildTimestamp, license)
}

func main() {
	desc := fmt.Sprintf("%s\n%s", description, getBuildInfo())
	cli := &CliConfig{}
	parser := kong.Must(cli,
		kong.Name("butterfish"),
		kong.Description(desc),
		kong.ConfigureHelp(kong.HelpOptions{
			Compact: true,
		}),
		kong.Vars{
			"shell_help": shell_help,
		})

	kongCtx, err := parser.Parse(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}

	cmd := kongCtx.Command()
	if cmd == "completion <shell>" {
		switch cli.Completion.Shell {
		case "bash":
			fmt.Print(bashCompletion)
		case "zsh":
			fmt.Print(zshCompletion)
		case "fish":
			fmt.Print(fishCompletion)
		default:
			log.Fatalf("unsupported shell: %s", cli.Completion.Shell)
		}
		return
	}

	config := makeButterfishConfig(cli)
	config.BuildInfo = getBuildInfo()
	ctx := context.Background()

	errorWriter := util.NewStyledWriter(os.Stderr, config.Styles.Error)

	switch cmd {
	case "shell":
		logfileName := util.InitLogging(ctx)
		fmt.Printf("Logging to %s\n", logfileName)

		alreadyRunning := os.Getenv("BUTTERFISH_SHELL")
		if alreadyRunning != "" {
			fmt.Fprintf(errorWriter, "Butterfish shell is already running, cannot wrap shell again (detected with BUTTERFISH_SHELL env var).\n")
			os.Exit(8)
		}

		shell := os.Getenv("SHELL")
		if cli.Shell.Bin != "" {
			shell = cli.Shell.Bin
		}
		if shell == "" {
			fmt.Fprintf(errorWriter, "No shell found, please specify one with -b or $SHELL\n")
			os.Exit(7)
		}

		config.ShellBinary = shell
		
		// Load model configs from env first
		loadModelConfig(config)
		
		// Command line args override env settings
		if cli.Shell.Model != "" {
			config.ShellPromptModel = cli.Shell.Model
		}
		if cli.Shell.AutosuggestModel != "" {
			config.ShellAutosuggestModel = cli.Shell.AutosuggestModel
		}
		
		config.ShellAutosuggestEnabled = !cli.Shell.AutosuggestDisabled
		config.ShellAutosuggestTimeout = time.Duration(cli.Shell.AutosuggestTimeout) * time.Millisecond
		config.ShellNewlineAutosuggestTimeout = time.Duration(cli.Shell.NewlineAutosuggestTimeout) * time.Millisecond
		config.ColorDark = !cli.LightColor
		config.ShellMode = true
		config.ShellLeavePromptAlone = cli.Shell.NoCommandPrompt
		config.ShellMaxPromptTokens = cli.Shell.MaxPromptTokens
		config.ShellMaxHistoryBlockTokens = cli.Shell.MaxHistoryBlockTokens
		config.ShellMaxResponseTokens = cli.Shell.MaxResponseTokens

		bf.RunShell(ctx, config)

	default:
		if cli.Log {
			util.InitLogging(ctx)
		}
		butterfishCtx, err := bf.NewButterfish(ctx, config)
		if err != nil {
			fmt.Fprintf(errorWriter, err.Error())
			os.Exit(3)
		}

		err = butterfishCtx.ExecCommand(kongCtx, &cli.CliCommandConfig)
		if err != nil {
			butterfishCtx.StylePrintf(config.Styles.Error, "Error: %s\n", err.Error())
			os.Exit(4)
		}
	}
}
