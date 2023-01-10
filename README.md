# Butterfish

Let's make it easy to use LLM capabilities from the command line, with a bent towards software engineering.

## Philosophy

- Piggyback on CLI concepts, i.e. shell, files, dotfiles, etc. Support existing tools rather than replace, e.g. wrap the shell.
- Two execution paths: directly from command line with `butterfish <cmd>`, or in the Butterfish console.
- Use the Butterfish console as chat interface and server, wrapped shells are the clients, enabling the console to read output, check for errors, and inject commands from the server.
- Written in golang, I simply will not spend more time with pip or anaconda.
- This is experimental, unpolished, no backwards compatibility guarantee.
- External contribution highly encouraged.

## Features

### Simple GPT prompt terminal

```
butterfish prompt "[prompt]"
```

Example

```
> butterfish prompt "A golang hello world program:"
...
```

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

### Semantically summarize a specific file

Example:

```
> butterfish summarize main.go
...
```

This is similar to Langchain and GPTIndex functionality. We read in the file,
if it's short then we hand it directly to GPT and ask for a summary. If it's
longer then we break it into chunks and ask GPT for a list of facts from each
chunk (max 8 chunks), then concatenate facts and ask GPT for an overall
summary.

## Installation

TODO: will deploy via homebrew

## Dev Setup

```
brew install protoc protoc-gen-go protoc-gen-go-grpc
make
```

## Potential Features

[x] Automatically explain a shell error
[x] Summarize a specific file
[ ] Summarize a directory of files
[ ] Create and output embeddings for a specific file
[ ] Rewrite a specific file given a prompt (e.g. Add comments to a code file, Refactor code)
[ ] Generate and run a shell command using a prompt
[ ] Generate tests for a specific code file
