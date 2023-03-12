# Butterfish

Let's do useful things with LLMs from the command line, with a bent towards software engineering.

[![GoDoc](https://godoc.org/github.com/bakks/butterfish?status.svg)](https://godoc.org/github.com/bakks/butterfish)

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/summarize.gif" alt="Butterfish" width="500px" height="250px" />

## What is this thing?

- I want to call GPT from the command line, pipe input/output, index and manipulate local files, etc.
- I want to see the actual prompts.
- I want to accelerate how I write and run code.
- Not into Python, prefer Go.

Solution:

- This is an experimental MacOS/Linux command line tool for using GPT-3. You give it your OpenAI key and it runs queries.
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

Butterfish works on MacOS and Linux. You can install via Homebrew:

```bash
brew install bakks/bakks/butterfish
butterfish prompt "Is this thing working?"
```

You can also install with `go get`, which is recommended for Linux:

```bash
go get github.com/bakks/butterfish/cmd/butterfish
butterfish prompt "Is this thing working?"
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

## Examples

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
engineering. v0.0.19 darwin amd64 (commit 336665f) (built 2023-03-02T07:04:41Z)
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

  execremote [<command> ...]
    Execute a command in a wrapped shell, either passed in or in command
    register. This is specifically for Console Mode after you have run gencmd
    when you have a wrapped terminal open.

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
- name: watch_shell_output
  prompt: The following is output from a user inside a "{shell_name}" shell, the user
    ran the command "{command}", if the output contains an error then print the specific
    segment that is an error and explain briefly how to solve the error, otherwise
    respond with only "NOOP". "{output}"
  oktoreplace: true
- name: summarize
  prompt: |-

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

Butterfish supports creating embeddings for local files and caching them on disk. This is the strategy many projects have been using to add external context into LLM prompts.

You can build an index by running `butterfish index` in a specific directory. This will recursively find all non-binary files, split files into chunks, use the OpenAI embedding API to embed each chunk, and cache the embeddings in a file called `.butterfish_index` in each directory. You can then run `butterfish indexsearch '[search text]'`, which will embed the search text and then search cached embeddings for the most similar chunk. You can also run `butterfish indexquestion '[question]'`, which injects related snippets into a prompt.

You can run `butterfish index` again later to update the index, this will skip over files that haven't been recently changed. Running `butterfish clearindex` will recursively remove `.butterfish_index` files.

The `.butterfish_index` cache files are binary files written using the protobuf schema in `proto/butterfish.proto`. If you check out this repo you can then inspect specific index files with a command like:

```
protoc --decode DirectoryIndex butterfish/proto/butterfish.proto < .butterfish_index
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
