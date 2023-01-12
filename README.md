# Butterfish

Let's do useful things with LLMs from the command line, with a bent towards software engineering.

<img src="https://github.com/bakks/butterfish/raw/main/assets/butterfish.png" alt="Butterfish" width="300px" height="300px" />

## Philosophy

- You can get pretty far by giving GPT some context information and asking directly for something useful, "e.g. does this shell output contain an error? '...'". A human must check output but becomes faster overall given this support.
- We want to distill LLM concepts for Unix command lines - shell, files, dotfiles, etc. Support existing tools rather than replace, e.g. wrap a shell and give you useful info without explicit invocation.
- We're going to experiment with many modes of LLM invocation:
  - Mode 1: directly from command line with `butterfish <cmd>`, e.g. `butterfish gencmd 'list all .go files in current directory'`.
  - Mode 2: The butterfish console, a persistent window that allows you to execute the CLI functionality but with persistent context.
  - Mode 3: Wrap a local command and control it from the console, e.g. tell you if you have an error.
- Written in golang, my opinion is that Python package management and distribution is very broken.
- This is experimental, unpolished, no backwards compatibility guarantee.
- External contribution and feedback highly encouraged. Submit a PR.

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

- [x] Automatically explain a shell error
- [x] Summarize a specific file
- [ ] Summarize a directory of files
- [ ] Create and output embeddings for a specific file
- [ ] Rewrite a specific file given a prompt (e.g. Add comments to a code file, Refactor code)
- [ ] Generate and run a shell command using a prompt
- [ ] Generate tests for a specific code file
