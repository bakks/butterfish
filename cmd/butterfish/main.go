package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/joho/godotenv"
	"github.com/mitchellh/go-homedir"

	//_ "net/http/pprof"

	bf "github.com/bakks/butterfish/butterfish"
	"github.com/bakks/butterfish/util"
)

var ( // these are filled in at build time
	BuildVersion   string
	BuildCommit    string
	BuildTimestamp string
)

const description = `Do useful things with LLMs from the command line, with a bent towards software engineering.

Butterfish is a command line tool for working with LLMs. It has two modes: CLI command mode, used to prompt LLMs, summarize files, and manage embeddings, and Shell mode: Wraps your local shell to provide easy prompting and autocomplete.

Butterfish stores an OpenAI auth token at ~/.config/butterfish/butterfish.env and the prompt wrappers it uses at ~/.config/butterfish/prompts.yaml. Butterfish logs to the system temp dir, usually to /var/tmp/butterfish.log.

To print the full prompts and responses from the OpenAI API, use the --verbose flag. Support can be found at https://github.com/bakks/butterfish.

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
	Verbose VerboseFlag      `short:"v" default:"false" help:"Verbose mode, prints full LLM prompts (sometimes to log file). Use multiple times for more verbosity, e.g. -vv."`
	Version kong.VersionFlag `short:"V" help:"Print version information and exit."`

	Shell struct {
		Bin                   string `short:"b" help:"Shell to use (e.g. /bin/zsh), defaults to $SHELL."`
		PromptModel           string `short:"m" default:"gpt-3.5-turbo" help:"Model for when the user manually enters a prompt."`
		AutosuggestDisabled   bool   `short:"A" default:"false" help:"Disable autosuggest."`
		AutosuggestModel      string `short:"a" default:"gpt-3.5-turbo-instruct" help:"Model for autosuggest"`
		AutosuggestTimeout    int    `short:"t" default:"400" help:"Delay after typing before autosuggest (lower values trigger more calls and are more expensive)."`
		NoCommandPrompt       bool   `short:"p" default:"false" help:"Don't change command prompt (shell PS1 variable). If not set, an emoji will be added to the prompt as a reminder you're in Shell Mode."`
		LightColor            bool   `short:"l" default:"false" help:"Light color mode, appropriate for a terminal with a white(ish) background"`
		MaxHistoryBlockTokens int    `short:"H" default:"512" help:"Maximum number of tokens of each block of history. For example, if a command has a very long output, it will be truncated to this length when sending the shell's history."`
	} `cmd:"" help:"${shell_help}"`

	// We include the cliConsole options here so that we can parse them and hand them
	// to the console executor, even though we're in the shell context here
	bf.CliCommandConfig
}

// Open a log file named butterfish.log in a temporary directory
func initLogging(ctx context.Context) string {
	// Check if the /var/log dir exists
	logDir := "/var/tmp"
	_, err := os.Stat(logDir)
	if err != nil {
		// Create a temporary directory to hold the log file
		logDir, err = ioutil.TempDir("", "butterfish")
		if err != nil {
			panic(err)
		}
	}

	// Create a log file in the temporary directory
	filename := filepath.Join(logDir, "butterfish.log")
	logFile, err := os.OpenFile(filename,
		os.O_RDWR|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		panic(err)
	}

	// Set the log output to the log file
	log.SetOutput(logFile)

	// Best effort to close the log file when the program exits
	go func() {
		<-ctx.Done()
		if logFile != nil {
			logFile.Close()
		}
	}()

	return filename
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
	err = os.MkdirAll(filepath.Dir(path), 0755)
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

func makeButterfishConfig(options *CliConfig) *bf.ButterfishConfig {
	config := bf.MakeButterfishConfig()
	config.OpenAIToken = getOpenAIToken()
	config.PromptLibraryPath = defaultPromptPath

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
	// start pprof server in goroutine
	// go func() {
	// 	log.Println(http.ListenAndServe("localhost:6060", nil))
	// }()

	desc := fmt.Sprintf("%s\n%s", description, getBuildInfo())
	cli := &CliConfig{}

	cliParser, err := kong.New(cli,
		kong.Name("butterfish"),
		kong.Description(desc),
		kong.UsageOnError(),
		kong.Vars{
			"shell_help": shell_help,
			"version":    getBuildInfo(),
		})

	if err != nil {
		panic(err)
	}

	parsedCmd, err := cliParser.Parse(os.Args[1:])
	cliParser.FatalIfErrorf(err)

	config := makeButterfishConfig(cli)
	config.BuildInfo = getBuildInfo()
	ctx := context.Background()

	errorWriter := util.NewStyledWriter(os.Stderr, config.Styles.Error)

	switch parsedCmd.Command() {
	case "shell":
		logfileName := initLogging(ctx)
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
		config.ShellPromptModel = cli.Shell.PromptModel
		config.ShellAutosuggestEnabled = !cli.Shell.AutosuggestDisabled
		config.ShellAutosuggestModel = cli.Shell.AutosuggestModel
		config.ShellAutosuggestTimeout = time.Duration(cli.Shell.AutosuggestTimeout) * time.Millisecond
		config.ShellColorDark = !cli.Shell.LightColor
		config.ShellMode = true
		config.ShellLeavePromptAlone = cli.Shell.NoCommandPrompt
		config.ShellMaxHistoryBlockTokens = cli.Shell.MaxHistoryBlockTokens

		bf.RunShell(ctx, config)

	default:
		butterfishCtx, err := bf.NewButterfish(ctx, config)
		if err != nil {
			fmt.Fprintf(errorWriter, err.Error())
			os.Exit(3)
		}
		//butterfishCtx.Config.Styles.PrintTestColors()

		err = butterfishCtx.ExecCommand(parsedCmd, &cli.CliCommandConfig)

		if err != nil {
			butterfishCtx.StylePrintf(config.Styles.Error, "Error: %s\n", err.Error())
			os.Exit(4)
		}
	}
}
