package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/joho/godotenv"
	"github.com/mitchellh/go-homedir"

	bf "github.com/bakks/butterfish/butterfish"
	"github.com/bakks/butterfish/util"
)

var ( // these are filled in at build time
	BuildVersion   string
	BuildArch      string
	BuildCommit    string
	BuildOs        string
	BuildTimestamp string
)

const description = `Do useful things with LLMs from the command line, with a bent towards software engineering.`
const license = "MIT License - Copyright (c) 2023 Peter Bakkum"
const defaultEnvPath = "~/.config/butterfish/butterfish.env"
const defaultPromptPath = "~/.config/butterfish/prompts.yaml"

// Kong configuration for shell arguments (shell meaning when butterfish is
// invoked, rather than when we're inside a butterfish console).
// Kong will parse os.Args based on this struct.
type CliConfig struct {
	Verbose bool `short:"v" default:"false" help:"Verbose mode, prints full LLM prompts."`

	Shell struct {
		Bin string `short:"b" help:"Shell to use (e.g. /bin/zsh), defaults to $SHELL."`
	} `cmd:"" help:"Start the Butterfish shell wrapper, which lets you run a prompt at any time by starting your command with a capital letter and uses recent context to autosuggest shell commands."`

	// We include the cliConsole options here so that we can parse them and hand them
	// to the console executor, even though we're in the shell context here
	bf.CliCommandConfig
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
	config.Verbose = options.Verbose
	config.OpenAIToken = getOpenAIToken()
	config.PromptLibraryPath = defaultPromptPath

	return config
}

func getBuildInfo() string {
	return fmt.Sprintf("%s %s %s\n(commit %s) (built %s)\n%s\n", BuildVersion, BuildOs, BuildArch, BuildCommit, BuildTimestamp, license)
}

func main() {
	desc := fmt.Sprintf("%s\n%s", description, getBuildInfo())
	cli := &CliConfig{}

	cliParser, err := kong.New(cli,
		kong.Name("butterfish"),
		kong.Description(desc),
		kong.UsageOnError())
	if err != nil {
		panic(err)
	}

	parsedCmd, err := cliParser.Parse(os.Args[1:])
	cliParser.FatalIfErrorf(err)

	config := makeButterfishConfig(cli)
	ctx := context.Background()

	errorWriter := util.NewStyledWriter(os.Stderr, config.Styles.Error)

	switch parsedCmd.Command() {
	case "shell":
		initLogging(ctx)
		shell := os.Getenv("SHELL")
		if cli.Shell.Bin != "" {
			shell = cli.Shell.Bin
		}
		if shell == "" {
			fmt.Fprintf(errorWriter, "No shell found, please specify one with -b or $SHELL")
			os.Exit(7)
		}

		bf.RunShell(ctx, config, shell)

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
