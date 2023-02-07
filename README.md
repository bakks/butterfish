# Butterfish

Let's do useful things with LLMs from the command line, with a bent towards software engineering.

<img src="https://github.com/bakks/butterfish/raw/main/assets/butterfish.png" alt="Butterfish" width="300px" height="300px" />

## What is this thing?

- There's voluminous LLM (GPT-3, etc) experimentation happening now, but I want something that enables Unix-like command line use of LLMs with transparency into the actual prompts. I write code in tmux/neovim, I want to be able to use LLMs without switching a browser.
- Let's LLM concepts for Unix command lines - shell, files, dotfiles, etc. Let's use pipes!
- Not a Python guy.

Solution:

- This is a MacOS command line tool for using GPT-3, a testbed for LLM strategies. You give it your OpenAI key and it runs queries.
- Currently this supports raw prompting, generating shell commands, embedding and caching embeddings for a directory tree,
  searching embeddings, watching output.
- Experimenting with several modes of LLM invocation:
  - Mode 1: directly from command line with `butterfish <cmd>`, e.g. `butterfish gencmd 'list all .go files in current directory'`.
  - Mode 2: The butterfish console, a persistent window that allows you to execute the CLI functionality but with persistent context.
  - Mode 3: Wrap a local command and control it from the console, e.g. run a shell, another shell solves the error you just saw.
- External contribution and feedback highly encouraged. Submit a PR! This is something of a playground for my LLM ideas, and others are welcome to make it their playground as well.

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

## Commands

Here's the command help:

```bash
> butterfish --help

Usage: butterfish <command>

Let's do useful things with LLMs from the command line,
with a bent towards software engineering.

Flags:
  -h, --help       Show context-sensitive help.
  -v, --verbose    Verbose mode, prints full LLM prompts.

Commands:
  wrap <cmd>
    Wrap a command (e.g. zsh) to expose to Butterfish.

  console
    Start a Butterfish console and server.

  prompt [<prompt> ...]
    Run an LLM prompt without wrapping, stream results back. Accepts piped input. This is a straight-through call to the LLM from the command line with a given
    prompt. It's recommended that you wrap the prompt with quotes. This defaults to the text-davinci-003.

  summarize [<files> ...]
    Semantically summarize a list of files (or piped input). We read in the file, if it's short then we hand it directly to the LLM and ask for a summary.
    If it's longer then we break it into chunks and ask for a list of facts from each chunk (max 8 chunks), then concatenate facts and ask GPT for an overall
    summary.

  gencmd <prompt> ...
    Generate a shell command from a prompt, i.e. pass in what you want, a shell command will be generated. Accepts piped input. You can use the -f command to
    execute it sight-unseen.

  rewrite <prompt>
    Rewrite a file using a prompt, must specify either a file path or provide piped input, and can output to stdout, output to a given file, or edit the input
    file in-place.

  index [<paths> ...]
    Recursively index the current directory using embeddings. This will read each file, split it into chunks, embed the chunks, and write a .butterfish_index
    file to each directory caching the embeddings. If you re-run this it will skip over previously embedded files unless you force a re-index. This implements
    an exponential backoff if you hit OpenAI API rate limits.

  exec [<command> ...]
    Execute a command, either passed in or in command register. This is specifically for Console after you've run gencmd.

  execremote [<command> ...]
    Execute a command in a wrapped shell, either passed in or in command register. This is specifically for Console mode after you've run gencmd when you have
    a wrapped terminal open.

  clearindex [<paths> ...]
    Clear paths from the index, both from the in-memory index (if in Console mode) and to delete .butterfish_index files.

  loadindex [<paths> ...]
    Load paths into the index. This is specifically for Console mode when you want to load a set of cached indexes into memory.

  showindex [<paths> ...]
    Show which files are present in the loaded index. You can pass in a path but it defaults to the current directory.

  indexsearch <query>
    Search embedding index and return relevant file snippets.

  indexquestion <question>
    Ask a question using the embeddings index. This fetches text snippets from the index and passes them to the LLM to generate an answer, thus you need to run
    the index command first.

Run "butterfish <command> --help" for more information on a command.


```

### `prompt`

```bash
butterfish prompt "[prompt]"
```

This is a straight-through call to the LLM from the command line with a given prompt.

Example:

```bash
> butterfish prompt "A golang hello world program:"
...
```

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/prompt.gif" alt="Butterfish" width="500px" height="250px" />

### `summarize`

```bash
butterfish summarize [files...]
```

Example:

```bash
> butterfish summarize main.go
...
```

Semantically summarize a set of paths.
This is similar to Langchain and GPTIndex functionality. We read in the file,
if it's short then we hand it directly to GPT and ask for a summary. If it's
longer then we break it into chunks and ask GPT for a list of facts from each
chunk (max 8 chunks), then concatenate facts and ask GPT for an overall
summary.

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/summarize.gif" alt="Butterfish" width="500px" height="250px" />

### Watch console output, make suggestions

Add gif here

First start the Butterfish console:

```
> butterfish console
```

In another terminal start a wrapped shell:

```
> butterfish wrap zsh
```

Now when you run commands in the wrapped shell, the console will automatically
attempt detection of problems/errors and offer suggestions.

Implementation is dumb: we grab stdout from the wrapped shell and if it's long
enough we put it in a prompt and ask GPT if there is a problem, and to offer
advice if so.

### Embeddings

```
protoc --decode DirectoryIndex butterfish/proto/butterfish.proto < .butterfish_index
```

## Installation

TODO: will deploy via homebrew

## Dev Setup

```
brew install protoc protoc-gen-go protoc-gen-go-grpc
make
```

## Potential Features

- [x] Automatically explain a shell error
- [x] Summarize a specific file
- [ ] Summarize a directory of files
- [ ] Create and output embeddings for a specific file
- [ ] Rewrite a specific file given a prompt (e.g. Add comments to a code file, Refactor code)
- [ ] Generate and run a shell command using a prompt
- [ ] Generate tests for a specific code file
