# 🐠 Butterfish Shell

A shell with AI superpowers

[![Website](https://img.shields.io/badge/website-https://butterfi.sh-blue)](https://butterfi.sh) [![GoDoc](https://godoc.org/github.com/bakks/butterfish?status.svg)](https://godoc.org/github.com/bakks/butterfish) [![Latest Version](https://img.shields.io/github/v/release/bakks/butterfish)](https://github.com/bakks/butterfish/releases) [![@pbbakkum](https://img.shields.io/badge/Updates%20at-%20%40pbbakkum-blue?style=flat&logo=twitter)](https://twitter.com/pbbakkum)

## What is this thing?

Butterfish is for people who work from the command line, it adds AI prompting to your shell (bash, zsh) with OpenAI. Think Github Copilot for shell.

Here's how it works: use your shell as normal, start a command with a capital letter to prompt the AI. The AI sees the shell history, so you can ask contextual questions like "why did that command fail?".

This is a magical UX pattern -- you get high-context AI help exactly when you want it, NO COPY/PASTING.

### What can you do with Butterfish Shell?

-   "Give me a command to do x"
-   "Why did that command fail?"
-   "!Run make in this directory, debug problems" (this acts as an agent)
-   Autocomplete shell commands (this uses recent history)
-   "Give me a pasta recipe" (this is a ChatGPT interface so it's not just for shell stuff!)

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/shell3.gif" alt="Demo of Butterfish Shell" width="500px" height="250px" />

Feedback and external contribution is very welcome! Butterfish is open source under the MIT license. We hope that you find it useful!

### Prompt Transparency

Many AI-enabled products obscure the prompt (instructional text) sent to the AI model. Butterfish makes this transparent and configurable.

To see the raw AI requests / responses you can run Butterfish in verbose mode (`butterfish shell -v`) and watch the log file (`/var/tmp/butterfish.log` on MacOS). For more verbosity, use `-vv`.

To configure the prompts you can edit `~/.config/butterfish/prompts.yaml`.

<img src="https://github.com/bakks/butterfish/raw/main/assets/verbose.png" alt="The verbose output of Butterfish Shell showing raw AI prompts" height="400px" />

## Installation / Authentication

Butterfish works on MacOS and Linux. You can install via Homebrew on MacOS:

```bash
brew install bakks/bakks/butterfish
butterfish prompt "Is this thing working?"
```

You can also install with `go install`:

```bash
go install github.com/bakks/butterfish/cmd/butterfish@latest
$(go env GOPATH)/bin/butterfish prompt "Is this thing working?"
```

The first invocation will prompt you to paste in an OpenAI API secret key. You can get an OpenAI key at [https://platform.openai.com/account/api-keys](https://platform.openai.com/account/api-keys).

The key will be written to `~/.config/butterfish/butterfish.env`, which looks like:

```
OPENAI_TOKEN=sk-foobar
```

It may also be useful to alias the `butterfish` command to something shorter. If you add the following line to your `~/.zshrc` or `~/.bashrc` file then you can run it with only `bf`.

```
alias bf="butterfish"
```

## Shell Mode

How does this work? Shell mode _wraps_ your shell rather than replacing it.

-   You run `butterfish shell` and use your existing shell as normal
-   You start a command with a capital letter to prompt the LLM, e.g. "How do I do..."
-   You can autocomplete commands and prompt questions with `Tab`
-   Prompts and autocomplete use local context for answers, like ChatGPT

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/shell2.gif" alt="Butterfish" width="500px" height="250px" />

This pattern is shockingly effective - you can ask the LLM to solve the error that just printed, and if it suggests a command then autocomplete that command. The shell history is the LLM chat history!

```bash
> butterfish shell --help
Usage: butterfish shell

Start the Butterfish shell wrapper. This wraps your existing shell, giving
you access to LLM prompting by starting your command with a capital letter.
LLM calls include prior shell context. This is great for keeping a chat-like
terminal open, sending written prompts, debugging commands, and iterating on
past actions.

Use:
  - Type a normal command, like 'ls -l' and press enter to execute it
  - Start a command with a capital letter to send it to GPT, like 'How do I
    recursively find local .py files?'
  - Autosuggest will print command completions, press tab to fill them in
  - GPT will be able to see your shell history, so you can ask contextual
    questions like 'why didn't my last command work?'
  - Start a command with ! to enter Goal Mode, in which GPT will act as an Agent
    attempting to accomplish your goal by executing commands, for example '!Run
    make in this directory and debug any problems'.
  - Start a command with !! to enter Unsafe Goal Mode, in which GPT will execute
    commands without confirmation. USE WITH CAUTION.

Here are special Butterfish commands:
  - Help : Give hints about usage.
  - Status : Show the current Butterfish configuration.
  - History : Print out the history that would be sent in a GPT prompt.

If you don't have OpenAI free credits then you'll need a subscription and you'll
need to pay for OpenAI API use. If you're using Shell Mode, autosuggest will
probably be the most expensive part. You can reduce spend here by disabling
shell autosuggest (-A) or increasing the autosuggest timeout (e.g. -t 2000).

Flags:
  -h, --help                       Show context-sensitive help.
  -v, --verbose                    Verbose mode, prints full LLM prompts
                                   (sometime to log file). Use multiple times
                                   for more verbosity, e.g. -vv.
  -V, --version                    Print version information and exit.

  -b, --bin=STRING                 Shell to use (e.g. /bin/zsh), defaults to
                                   $SHELL.
  -m, --prompt-model="gpt-3.5-turbo"
                                   Model for when the user manually enters a
                                   prompt.
  -A, --autosuggest-disabled       Disable autosuggest.
  -a, --autosuggest-model="text-davinci-003"
                                   Model for autosuggest
  -t, --autosuggest-timeout=500    Delay after typing before autosuggest (lower
                                   values trigger more calls and are more
                                   expensive).
  -p, --no-command-prompt          Don't change command prompt (shell PS1
                                   variable). If not set, an emoji will be added
                                   to the prompt as a reminder you're in Shell
                                   Mode.
  -l, --light-color                Light color mode, appropriate for a terminal
                                   with a white(ish) background
  -H, --max-history-block-tokens=512
                                   Maximum number of tokens of each block of
                                   history. For example, if a command has a very
                                   long output, it will be truncated to this
                                   length when sending the shell's history.

```

Shell mode defaults to using `gpt-3.5-turbo` for prompting, if you have access to GPT-4 you can use it with:

```bash
butterfish shell -m gpt-4
```

### Goal Mode

If you're in Shell Mode you can start an agent to accomplish a goal by
triggering Goal Mode. Start a command with `!`, as in `!Fix that bug`. Goal
Mode will populate a command in your shell, which you can execute with `Enter`,
or you can edit the command, or give feedback to the agent by doing a shell
prompt (by starting a command with a capital letter). Goal Mode will exit
if it decides the goal is met or impossible, or you can manually exit with
`Ctrl-C`.

You can trigger Unsafe Goal Mode by starting a command with `!!`, which will
execute commands without confirmation, and is thus potentially dangerous.

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/goal.gif" alt="Butterfish Goal Mode trying multiple strategies to accomplish a goal." width="500px" height="250px" />

#### Goal Mode Examples

How well does this work? Mileage will vary. Your success rate will be
higher with simpler goals and more guidance about how to accomplish them.

The advantages of this feature are that the agent can see your shell history
and so it has context of what you're doing manually and can take over. If a
command fails the agent will tweak it and try again.

Some disadvantages are that the agent is biased towards specific versions of
commands and may have to experiment to get it right, for example the flags
for `grep` on MacOS are different than on most Linux implementations. The agent
isn't very effective at manipulating large text files like code files, so you
will want to be conscious of the context it needs to be successful.

Here are some goals that work well:

-   `!Recursively list the golang files in this directory`
-   `!Find the hidden files in this directory and ask me if I want to delete them`. This will generally print some things and then wait for user input (provided by prompting starting with a capital letter).
-   `!Show me what process is using the most memory`

Here are some goals that work _sometimes_:

-   `!Run make in this dir, debug problems`
-   `!Install python dependencies for this project`
-   `!Create a list of the top 3 hacker news headlines, including a link. Use the pup command to parse them out of HTML`

## CLI Examples

Shell Mode is the primary focus of Butterfish but it also includes more specific command line utilities for prompting, generating commands, summarizing text, and managing embeddings of local files.

### `prompt` - Straightforward LLM prompt

Examples:

```bash
butterfish prompt "Write me a poem about placeholder text"
echo "Explain unix pipes to me:" | butterfish prompt
cat go.mod | butterfish prompt "Explain what this go project file contains:"
```

```bash
> butterfish prompt --help
Usage: butterfish prompt [<prompt> ...]

Run an LLM prompt without wrapping, stream results back. This is a
straight-through call to the LLM from the command line with a given prompt.
This accepts piped input, if there is both piped input and a prompt then they
will be concatenated together (prompt first). It is recommended that you wrap
the prompt with quotes. The default GPT model is gpt-3.5-turbo.

Arguments:
  [<prompt> ...]    Prompt to use.

Flags:
  -h, --help                     Show context-sensitive help.
  -v, --verbose                  Verbose mode, prints full LLM prompts (sometime
                                 to log file). Use multiple times for more
                                 verbosity, e.g. -vv.
  -V, --version                  Print version information and exit.

  -m, --model="gpt-3.5-turbo"    GPT model to use for the prompt.
  -n, --num-tokens=1024          Maximum number of tokens to generate.
  -T, --temperature=0.7          Temperature to use for the prompt, higher
                                 temperature indicates more freedom/randomness
                                 when generating each token.

```

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/prompt.gif" alt="Butterfish" width="500px" height="250px" />

### `gencmd` - Generate a shell command

Use the `-f` flag to execute sight unseen.

```
butterfish gencmd -f "Find all of the go files in the current directory, recursively"
```

```bash
> butterfish gencmd --help
Usage: butterfish gencmd <prompt> ...

Generate a shell command from a prompt, i.e. pass in what you want, a shell
command will be generated. Accepts piped input. You can use the -f command to
execute it sight-unseen.

Arguments:
  <prompt> ...    Prompt describing the desired shell command.

Flags:
  -h, --help       Show context-sensitive help.
  -v, --verbose    Verbose mode, prints full LLM prompts (sometime to log file).
                   Use multiple times for more verbosity, e.g. -vv.
  -V, --version    Print version information and exit.

  -f, --force      Execute the command without prompting.

```

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/gencmd.gif" alt="Butterfish" width="500px" height="250px" />

### `summarize` - Get a semantic summary of file content

If necessary, this command will split the file into chunks, summarize chunks, then produce a final summary.

```
butterfish summarize README.md
cat go/main.go | butterfish summarize
```

```bash
> butterfish summarize --help
Usage: butterfish summarize [<files> ...]

Semantically summarize a list of files (or piped input). We read in the file,
if it is short then we hand it directly to the LLM and ask for a summary. If it
is longer then we break it into chunks and ask for a list of facts from each
chunk (max 8 chunks), then concatenate facts and ask GPT for an overall summary.

Arguments:
  [<files> ...]    File paths to summarize.

Flags:
  -h, --help               Show context-sensitive help.
  -v, --verbose            Verbose mode, prints full LLM prompts (sometime to
                           log file). Use multiple times for more verbosity,
                           e.g. -vv.
  -V, --version            Print version information and exit.

  -c, --chunk-size=3600    Number of bytes to summarize at a time if the file
                           must be split up.
  -C, --max-chunks=8       Maximum number of chunks to summarize from a specific
                           file.

```

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/summarize.gif" alt="Butterfish" width="500px" height="250px" />

### `exec` - Run a command and suggest a fix if it fails

```
butterfish exec 'find -nam foobar'
```

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/exec.gif" alt="Butterfish" width="500px" height="250px" />

### `index` - Index local files with embeddings

```
butterfish index .
butterfish indexsearch "compare indexed embeddings against this string"
butterfish indexquestion "inject similar indexed embeddings into a prompt"
```

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/index.gif" alt="Butterfish" width="500px" height="250px" />

## Commands

Here's the command help:

```
> butterfish --help
Usage: butterfish <command>

Do useful things with LLMs from the command line, with a bent towards software
engineering.

Butterfish is a command line tool for working with LLMs. It has two modes: CLI
command mode, used to prompt LLMs, summarize files, and manage embeddings, and
Shell mode: Wraps your local shell to provide easy prompting and autocomplete.

Butterfish stores an OpenAI auth token at ~/.config/butterfish/butterfish.env
and the prompt wrappers it uses at ~/.config/butterfish/prompts.yaml. Butterfish
logs to the system temp dir, usually to /var/tmp/butterfish.log.

To print the full prompts and responses from the OpenAI API, use the --verbose
flag. Support can be found at https://github.com/bakks/butterfish.

If you don't have OpenAI free credits then you'll need a subscription and you'll
need to pay for OpenAI API use. If you're using Shell Mode, autosuggest will
probably be the most expensive part. You can reduce spend here by disabling
shell autosuggest (-A) or increasing the autosuggest timeout (e.g. -t 2000).
See "butterfish shell --help".

v0.1.6 darwin amd64 (commit 6d4dafe) (built 2023-08-17T08:21:44Z) MIT License -
Copyright (c) 2023 Peter Bakkum

Flags:
  -h, --help       Show context-sensitive help.
  -v, --verbose    Verbose mode, prints full LLM prompts (sometime to log file).
                   Use multiple times for more verbosity, e.g. -vv.
  -V, --version    Print version information and exit.

Commands:
  shell
    Start the Butterfish shell wrapper. This wraps your existing shell, giving
    you access to LLM prompting by starting your command with a capital letter.
    LLM calls include prior shell context. This is great for keeping a chat-like
    terminal open, sending written prompts, debugging commands, and iterating on
    past actions.

    Use:
      - Type a normal command, like 'ls -l' and press enter to execute it
      - Start a command with a capital letter to send it to GPT, like 'How do I
        recursively find local .py files?'
      - Autosuggest will print command completions, press tab to fill them in
      - GPT will be able to see your shell history, so you can ask contextual
        questions like 'why didn't my last command work?'
      - Start a command with ! to enter Goal Mode, in which GPT will act as
        an Agent attempting to accomplish your goal by executing commands,
        for example '!Run make in this directory and debug any problems'.
      - Start a command with !! to enter Unsafe Goal Mode, in which GPT will
        execute commands without confirmation. USE WITH CAUTION.

    Here are special Butterfish commands:
      - Help : Give hints about usage.
      - Status : Show the current Butterfish configuration.
      - History : Print out the history that would be sent in a GPT prompt.

    If you don't have OpenAI free credits then you'll need a subscription
    and you'll need to pay for OpenAI API use. If you're using Shell Mode,
    autosuggest will probably be the most expensive part. You can reduce spend
    here by disabling shell autosuggest (-A) or increasing the autosuggest
    timeout (e.g. -t 2000).

  plugin
    Run a ChatGPT Plugin client that allows remote command execution on the
    local machine.

  prompt [<prompt> ...]
    Run an LLM prompt without wrapping, stream results back. This is a
    straight-through call to the LLM from the command line with a given prompt.
    This accepts piped input, if there is both piped input and a prompt then
    they will be concatenated together (prompt first). It is recommended that
    you wrap the prompt with quotes. The default GPT model is gpt-3.5-turbo.

  summarize [<files> ...]
    Semantically summarize a list of files (or piped input). We read in the
    file, if it is short then we hand it directly to the LLM and ask for a
    summary. If it is longer then we break it into chunks and ask for a list of
    facts from each chunk (max 8 chunks), then concatenate facts and ask GPT for
    an overall summary.

  gencmd <prompt> ...
    Generate a shell command from a prompt, i.e. pass in what you want, a shell
    command will be generated. Accepts piped input. You can use the -f command
    to execute it sight-unseen.

  exec [<command> ...]
    Execute a command and try to debug problems. The command can either passed
    in or in the command register (if you have run gencmd in Console Mode).

  index [<paths> ...]
    Recursively index the current directory using embeddings. This will
    read each file, split it into chunks, embed the chunks, and write a
    .butterfish_index file to each directory caching the embeddings. If you
    re-run this it will skip over previously embedded files unless you force a
    re-index. This implements an exponential backoff if you hit OpenAI API rate
    limits.

  clearindex [<paths> ...]
    Clear paths from the index, both from the in-memory index (if in Console
    Mode) and to delete .butterfish_index files. Defaults to loading from the
    current directory but allows you to pass in paths to load.

  loadindex [<paths> ...]
    Load paths into the index. This is specifically for Console Mode when you
    want to load a set of cached indexes into memory. Defaults to loading from
    the current directory but allows you to pass in paths to load.

  showindex [<paths> ...]
    Show which files are present in the loaded index. You can pass in a path but
    it defaults to the current directory.

  indexsearch <query>
    Search embedding index and return relevant file snippets. This uses the
    embedding API to embed the search string, then does a brute-force cosine
    similarity against every indexed chunk of text, returning those chunks and
    their scores.

  indexquestion <question>
    Ask a question using the embeddings index. This fetches text snippets from
    the index and passes them to the LLM to generate an answer, thus you need to
    run the index command first.

Run "butterfish <command> --help" for more information on a command.

```

### Prompt Library

A goal of Butterfish is to make prompts transparent and easily editable. Butterfish will write a prompt library to `~/.config/butterfish/prompts.yaml` and load this every time it runs. You can edit prompts in that file to tweak them. If you edit a prompt then set `OkToReplace: false`, which prevents overwriting.

```
> head -n 8 ~/.config/butterfish/prompts.yaml
- name: shell_system_message
  prompt: You are an assistant that helps the user with a Unix shell. Give advice
    about commands that can be run and examples but keep your answers succinct.
  oktoreplace: true
- name: shell_autocomplete_command
  prompt: |-
    The user is asking for an autocomplete suggestion for this Unix shell command, respond with only the suggested command, which should include the original command text, do not add comments or quotations. Here is recent history:
    '''

```

If you want to see the exact communication between Butterfish and the OpenAI API then set the verbose flag (`-v`) when you run Butterfish, this will print the full prompt and response either to the terminal or to a log file.

#### Example

The `butterfish summarize` command gives you a semantic summary of a file. For example you can run `butterfish summarize ./go.mod`, and it will open that file and give you an English-language summary of what's in it.

When `summarize` runs, it wraps the file contents in the prompt (also there's some functionality for when it won't fit, but let's ignore that). In other words, it says something like "this is a raw text file, summarize it: '{content}'". But maybe this prompt isn't working well for you, or you want it to assume more things about the file, or you want the output to be different than a completely generic summary.

In that case, you can open `~/.config/butterfish/prompts.yaml`, find the prompt named `summarize`, and edit it. Once you edit you should set `oktoreplace` to `false`.

Let's try it - change the `summarize` prompt to say something like "Summarize in spanish", set `oktoreplace`, and then run `butterfish summarize [file]`.

Remember that if you run Butterfish in verbose mode (with `-v`), you will see the prompt when you run it!

### Embeddings

Example:

```
butterfish index .
butterfish indexsearch 'Lorem ipsem dolor sit amet'
butterfish indexquestion 'Lorem ipsem dolor sit amet?'
```

Butterfish supports creating embeddings for local files and caching them on disk. This is the strategy many projects have been using to add external context into LLM prompts.

You can build an index by running `butterfish index` in a specific directory. This will recursively find all non-binary files, split files into chunks, use the OpenAI embedding API to embed each chunk, and cache the embeddings in a file called `.butterfish_index` in each directory. You can then run `butterfish indexsearch '[search text]'`, which will embed the search text and then search cached embeddings for the most similar chunk. You can also run `butterfish indexquestion '[question]'`, which injects related snippets into a prompt.

You can run `butterfish index` again later to update the index, this will skip over files that haven't been recently changed. Running `butterfish clearindex` will recursively remove `.butterfish_index` files.

The `.butterfish_index` cache files are binary files written using the protobuf schema in `proto/butterfish.proto`. If you check out this repo you can then inspect specific index files with a command like:

```
protoc --decode DirectoryIndex butterfish/proto/butterfish.proto < .butterfish_index
```

#### Example

Let's say you have a software project repository that you want to embed, we'll call this project `helloworld`. First we can index it:

```
butterfish index /path/to/helloworld
```

That will run recursively, and you should see output as it calculates embeddings. Once those embeddings exist, go to the directory and check if you can use them:

```
cd /path/to/helloworld
butterfish indexsearch "printf 'hello world'"
```

This will search for embeddings that match the string you hand it. Hopefully these are relevant results!

Often you want to not only do that index search, but hand the results into a GPT prompt so that you can ask a question. In that case `butterfish indexquestion` uses the prompt both to search the embeddings, as a prompt to GPT to ask a question.

## Dev Setup

I've been developing Butterfish on an Intel Mac, but it should work fine on ARM Macs and probably work on Linux (untested). Here is how to get set up for development on MacOS:

```
brew install git go protoc protoc-gen-go protoc-gen-go-grpc
git clone https://github.com/bakks/butterfish
cd butterfish
make
./bin/butterfish prompt "Is this thing working?"
```
