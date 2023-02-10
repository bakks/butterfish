# Butterfish

Let's do useful things with LLMs from the command line, with a bent towards software engineering.

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/summarize.gif" alt="Butterfish" width="500px" height="250px" />

## What is this thing?

- I want to call GPT from the command line, pipe input/output, index and manipulate local files, etc.
- I want to see the actual prompts.
- I want to accelerate how I write and run code.
- Not into Python, prefer Go.

Solution:

- This is an experimental MacOS command line tool for using GPT-3. You give it your OpenAI key and it runs queries.
- What can you do with it?
  - Run raw GPT prompts.
  - Semantically summarize local content.
  - Generate and run shell commands.
  - Detect when a command fails and offer a fix.
  - Edit your prompts in a YAML file.
  - Generate and cache embeddings for text files.
  - Search and ask GPT questions based on those embeddings.
- Experimenting with several modes of LLM invocation:
  - Command Line: directly from command line with `butterfish <cmd>`, e.g. `butterfish gencmd 'list all .go files in current directory'`.
  - Persistent Console: The butterfish console, a persistent window for talking to LLMs.
  - Wrapped Shell: Wrap a local command and control it from the console, e.g. run a shell, another shell solves the error you just saw.
- External contribution and feedback highly encouraged. Submit a PR!

## Installation / Authentication

Butterfish works on MacOS and is installed via Homebrew:

```

brew install bakks/bakks/butterfish
butterfish prompt "Is this thing working?"

```

This should prompt you to paste in an OpenAI API secret key. You can get an OpenAI key at [https://platform.openai.com/account/api-keys](https://platform.openai.com/account/api-keys).

The key will be written to `~/.config/butterfish/butterfish.env`, which looks like:

```

OPENAI_TOKEN=sk-foobar

```

## Examples

### `prompt` - Straightforward LLM prompt

Examples:

```bash
butterfish prompt "Write me a poem about placeholder text"
echo "Explain unix pipes to me:" | butterfish prompt
```

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/prompt.gif" alt="Butterfish" width="500px" height="250px" />

### `gencmd` - Generate a shell command

Use the `-f` flag to execute sight unseen.

```
butterfish gencmd -f "Find all of the go files in the current directory, recursively"
```

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/gencmd.gif" alt="Butterfish" width="500px" height="250px" />

### `rewrite` - Rewrite a file with LLM instructions

```
butterfish rewrite -I "Add comments to all functions" < main.go
butterfish rewrite -i ./Makefile "Add a command for updating go modules"
```

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/rewrite.gif" alt="Butterfish" width="500px" height="250px" />

### `summarize` - Get a semantic summary of file content

```
butterfish summarize README.md
cat go/main.go | butterfish summarize
```

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/summarize.gif" alt="Butterfish" width="500px" height="250px" />

### `exec` - Run a command and suggest a fix if it fails

```
butterfish exec 'find -nam foobar'
```

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/exec.gif" alt="Butterfish" width="500px" height="250px" />

## Commands

Here's the command help:

```
> butterfish --help
Usage: butterfish <command>

Do useful things with LLMs from the command line, with a bent towards software
engineering. v0.0.10 darwin amd64 (commit ecdc5b2) (built 2023-02-10T03:18:11Z)
MIT License - Copyright (c) 2023 Peter Bakkum

Flags:
  -h, --help       Show context-sensitive help.
  -v, --verbose    Verbose mode, prints full LLM prompts.

Commands:
  wrap <cmd>
    Wrap a command (e.g. zsh) to expose to Butterfish.

  console
    Start a Butterfish console and server.

  prompt [<prompt> ...]
    Run an LLM prompt without wrapping, stream results back. Accepts piped
    input. This is a straight-through call to the LLM from the command line with
    a given prompt. It is recommended that you wrap the prompt with quotes.
    This defaults to the text-davinci-003.

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
    input file in-place.

  index [<paths> ...]
    Recursively index the current directory using embeddings. This will
    read each file, split it into chunks, embed the chunks, and write a
    .butterfish_index file to each directory caching the embeddings. If you
    re-run this it will skip over previously embedded files unless you force a
    re-index. This implements an exponential backoff if you hit OpenAI API rate
    limits.

  exec [<command> ...]
    Execute a command and try to debug problems. The command can either passed
    in or in the command register (if you have run gencmd in Console Mode).

  execremote [<command> ...]
    Execute a command in a wrapped shell, either passed in or in command
    register. This is specifically for Console Mode after you have run gencmd
    when you have a wrapped terminal open.

  clearindex [<paths> ...]
    Clear paths from the index, both from the in-memory index (if in Console
    Mode) and to delete .butterfish_index files.

  loadindex [<paths> ...]
    Load paths into the index. This is specifically for Console Mode when you
    want to load a set of cached indexes into memory.

  showindex [<paths> ...]
    Show which files are present in the loaded index. You can pass in a path but
    it defaults to the current directory.

  indexsearch <query>
    Search embedding index and return relevant file snippets.

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
- name: watch_shell_output
  prompt: The following is output from a user inside a "{shell_name}" shell, the user
    ran the command "{command}", if the output contains an error then print the specific
    segment that is an error and explain briefly how to solve the error, otherwise
    respond with only "NOOP". "{output}"
  oktoreplace: true

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

### Embeddings

Example:

```
butterfish index .
butterfish indexsearch 'Lorem ipsem dolor sit amet'
butterfish indexquestion 'Lorem ipsem dolor sit amet?'
```

Managing embeddings for later search is supported by Butterfish. This is the strategy many projects have been using to add external context into LLM prompts.

You can build an index by running `butterfish index` in a specific directory. This will recursively find all non-binary files, split files into chunks, use the OpenAI embedding API to embed each chunk, and cache the embeddings in a file called `.butterfish_index` in each directory. You can then run `butterfish indexsearch '[search text]'`, which will embed the search text and then search cached embeddings for the most similar chunk. You can also run `butterfish indexquestion '[question]'`, which injects related snippets into a prompt.

You can run `butterfish index` again later to update the index, this will skip over files that haven't been recently changed. Running `butterfish clearindex` will recursively remove `.butterfish_index` files.

The `.butterfish_index` cache files are binary files written using the protobuf schema in `proto/butterfish.proto`. If you check out this repo you can then inspect specific index files with a command like:

```
protoc --decode DirectoryIndex butterfish/proto/butterfish.proto < .butterfish_index
```

### Console Mode

I'm experimenting with having a persistent console window open that has context from other shells that you run. You open a console with `butterfish console`, and can then run Butterfish commands in a persistent window, e.g. `prompt '[prompt text]'`.

If you have a console open, you can then plug other shells into the console with `butterfish wrap [shellname]`. The console will watch the output of the shell and attempt to offer advice if it detects an error.

You can also generate commands in the console and run them on the other shell. For example:

```
gencmd "Pull down recent remote changes to the git repo and rebase my own commits on top of them"
execremote
```

## Dev Setup

I've been developing Butterfish on an Intel Mac, but it should work fine on ARM Macs and probably work on Linux (untested). Here is how to get set up for development on MacOS:

```
brew install git go protoc protoc-gen-go protoc-gen-go-grpc
git clone https://github.com/bakks/butterfish
cd butterfish
make
./bin/butterfish prompt "Is this thing working?"
```
