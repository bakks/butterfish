# Butterfish

Let's do useful things with LLMs from the command line, with a bent towards software engineering.

[![GoDoc](https://godoc.org/github.com/bakks/butterfish?status.svg)](https://godoc.org/github.com/bakks/butterfish) ![Latest Version](https://img.shields.io/github/v/release/bakks/butterfish)
Updates at [@pbbakkum](https://twitter.com/pbbakkum)

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/summarize.gif" alt="Butterfish" width="500px" height="250px" />

## What is this thing?

-   I want to call GPT from the command line, pipe input/output, index and manipulate local files, etc.
-   I want to see the actual prompts.
-   I want to accelerate how I write and run code.
-   Not into Python, prefer Go.

Solution:

-   This is an experimental MacOS/Linux command line tool for using GPT-3. You give it your OpenAI key and it runs queries.
-   What can you do with it?
    -   _Ask questions in your shell with recent history_
    -   Run raw GPT prompts.
    -   Semantically summarize local content.
    -   Generate and run shell commands.
    -   Detect when a command fails and offer a fix.
    -   Edit your prompts in a YAML file.
    -   Generate and cache embeddings for text files.
    -   Search and ask GPT questions based on those embeddings.
-   Experimenting with different modes of LLM invocation:
    -   Command Line: directly from command line with `butterfish <cmd>`, e.g. `butterfish gencmd 'list all .go files in current directory'`.
    -   Wrapped Shell: start with a Capital letter to prompt the LLM, prompts and autocomplete see recent history.
-   External contribution and feedback highly encouraged. Submit a PR!

## Installation / Authentication

Butterfish works on MacOS and Linux. You can install via Homebrew:

```bash
brew install bakks/bakks/butterfish
butterfish prompt "Is this thing working?"
```

You can also install with `go install`, which is recommended for Linux:

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

You might use your local terminal for lots of everyday tasks - building code, using git, logging into remote machines, etc. Do you remember off the top of your head how to unpackage a .tar.gz file? No one does!

Shell mode is an attempt to tightly integrate language models into your shell. Think Github Copilot for shell. The goals here are:

-   Make the user faster and more effective
-   Be unobtrusive, don't break current shell workflows
-   Don't require another window or using mouse
-   Use the recent shell history

How does this work? Shell mode _wraps_ your shell rather than replacing it.

-   You run `butterfish shell` and get a transparent layer on top of your normal shell.
-   You start a command with a capital letter to prompt the LLM, e.g. "How do I do..."
-   You can autocomplete commands and prompt questions
-   Prompts and autocomplete use local context for answers, like ChatGPT

This pattern is shockingly effective - you can ask the LLM to solve the error that just printed, and if it suggests a command then autocomplete that command.

This is also very new code and there are likely bugs, please log issues in github.

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
  -v, --verbose                    Verbose mode, prints full LLM prompts.

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
  -h, --max-history-block-tokens=512
                                   Maximum number of tokens of each block of
                                   history. For example, if a command has a very
                                   long output, it will be truncated to this
                                   length when sending the shell's history.

```

Shell mode defaults to using `gpt-3.5-turbo` for prompting, if you have access to GPT-4 you can use it with:

```bash
butterfish shell -m 'gpt-4' -w 6000
```

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/shell.gif" alt="Butterfish" width="500px" height="250px" />

## Plugin Mode

If you're looking for a way to accidentally delete your files, Plugin Mode is a way to grant ChatGPT plugins direct access to a machine. Example chat prompts:

-   What files are in my home directory?
-   Create a new Python project in ~/project using pip dependencies, provide a hello world script.

<img src="https://github.com/bakks/butterfish/raw/plugin/assets/plugin.png" alt="Plugin Demo" width="500px" />

This works by starting a local plugin client that connects to the Butterfish server and provides a session-specific token. You give the token to ChatGPT, which then is capable of executing commands on your local machine in response to a chat prompt.

This is brand new and should be used with _extreme caution_. Everything is done over TLS but this probably insecure in ways I haven't considered and is basically an RCE by design.

Here are local MacOS setup instructions:

```bash
brew install git go protoc protoc-gen-go protoc-gen-go-grpc
git clone https://github.com/bakks/butterfish
cd butterfish
git fetch
git checkout plugin
make

./bin/butterfish plugin
```

This should connect to the Butterfish server and give you a session specific token. Next:

1. Copy your session token, it is a UUID printed when the above runs
1. Go to [https://chat.openai.com/chat](https://chat.openai.com/chat)
1. Open the plugin menu > plugin store. _You must be gated into plugins_.
1. Click "Install an unverified plugin" in the bottom right corner
1. For the plugin domain, enter `butterfi.sh`
1. Approve unverified plugin - this is unverified
1. When it asks for your HTTP token, enter the copied session token. You will need to reinstall and paste in a new token for _each_ time you start the local plugin process.
1. You should now see the ChatGPT interface. ChatGPT will use its judgement for when to execute commands on your machine, which will be displayed in the output of the `butterfish plugin` command.

## CLI Examples

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
  -v, --verbose                  Verbose mode, prints full LLM prompts.

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
  -v, --verbose    Verbose mode, prints full LLM prompts.

  -f, --force      Execute the command without prompting.

```

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/gencmd.gif" alt="Butterfish" width="500px" height="250px" />

### `rewrite` - Rewrite a file with LLM instructions

```
butterfish rewrite -I "Add comments to all functions" < main.go
butterfish rewrite -i ./Makefile "Add a command for updating go modules"
```

```bash
> butterfish rewrite --help
Usage: butterfish rewrite <prompt>

Rewrite a file using a prompt, must specify either a file path or provide piped
input, and can output to stdout, output to a given file, or edit the input file
in-place. This command uses the OpenAI edit API rather than the completion API.

Arguments:
  <prompt>    Instruction to the model on how to rewrite.

Flags:
  -h, --help                 Show context-sensitive help.
  -v, --verbose              Verbose mode, prints full LLM prompts.

  -i, --inputfile=STRING     Source file for content to rewrite. If not set then
                             there must be piped input.
  -o, --outputfile=STRING    File to write the rewritten output to.
  -I, --inplace              Rewrite the input file in place. This is
                             potentially destructive, use with caution! Cannot
                             be set at the same time as the outputfile flag.
  -m, --model="code-davinci-edit-001"
                             GPT model to use for editing. At compile time
                             this should be either 'code-davinci-edit-001' or
                             'text-davinci-edit-001'.
  -T, --temperature=0.6      Temperature to use for the prompt, higher
                             temperature indicates more freedom/randomness when
                             generating each token.
  -c, --chunk-size=4000      Number of bytes to rewrite at a time if the file
                             must be split up.
  -C, --max-chunks=128       Maximum number of chunks to rewrite from a specific
                             file.

```

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/rewrite.gif" alt="Butterfish" width="500px" height="250px" />

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
  -v, --verbose            Verbose mode, prints full LLM prompts.

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
and the prompt wrappers it uses at ~/.config/butterfish/prompts.yaml.

To print the full prompts and responses from the OpenAI API, use the --verbose
flag. Support can be found at https://github.com/bakks/butterfish.

If you don't have OpenAI free credits then you'll need a subscription and you'll
need to pay for OpenAI API use. If you're using Shell Mode, autosuggest will
probably be the most expensive part. You can reduce spend here by disabling
shell autosuggest (-A) or increasing the autosuggest timeout (e.g. -t 2000).
See "butterfish shell --help".

v0.1.0 darwin amd64 (commit efb4b0a) (built 2023-06-07T01:45:25Z) MIT License -
Copyright (c) 2023 Peter Bakkum

Flags:
  -h, --help       Show context-sensitive help.
  -v, --verbose    Verbose mode, prints full LLM prompts.

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

  rewrite <prompt>
    Rewrite a file using a prompt, must specify either a file path or provide
    piped input, and can output to stdout, output to a given file, or edit the
    input file in-place. This command uses the OpenAI edit API rather than the
    completion API.

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

If you want to see the exact communication between Butterfish and the OpenAI API then set the verbose flag (`-v`) when you run Butterfish, this will print the full prompt and response. For example:

```
butterfish -v gencmd "find all go files"
Loaded 7 prompts from /Users/bakks/.config/butterfish/prompts.yaml
↑ ---
Write a shell command that accomplishes the following goal. Respond with only the shell command.
'''
find all go files
'''
...
```

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