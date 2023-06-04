# What is Butterfish Shell?

## The Short Answer

Butterfish Shell is an open-source way to embed OpenAI ChatGPT in your terminal shell without having to invoke it every time. It's easily accessible and can see your shell history.

| MacOS                                                     | Linux/MacOS                                                                                             |
| --------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| `brew install bakks/bakks/butterfish && butterfish shell` | `go install github.com/bakks/butterfish/cmd/butterfish@latest && $(go env GOPATH)/bin/butterfish shell` |

Once you're in Butterfish shell, talk to ChatGPT by starting a command with a _Capital Letter_. This uses your own OpenAI account directly. The code is open source and available at [github.com/bakks/butterfish](https://github.com/bakks/butterfish).

<img src="https://github.com/bakks/butterfish/raw/main/vhs/gif/shell.gif" alt="Butterfish" width="500px" height="250px" />

## The Long Answer

If you work with software you likely spend _some_ time in a terminal shell. The terminal is a direct window into your machine, and feels both powerful and tedious. You can do almost anything directly from the command line, as long as you correctly summon arcane unix commands.

I've been experimenting with getting LLMs (like ChatGPT) to help you with the command line, for example to suggest a command or summarize a file. But it feels clunky run something like `llm 'answer this question'` every time. And it should be able to see the history, I don't want to copy/paste stuff!

Here's my solution: put a transparent layer around the terminal shell that communicates with ChatGPT. You can prompt ChatGPT at any time, the anwsers include the right context, and you can do autosuggest at this layer, among other things. This feels like the right way to do a ChatGPT CLI: the AI is always there, it feels like magic.

I'm using Butterfish Shell for myself constantly, it might be useful for you so please try it and send feedback! This document is here to give you a mental model of what and why Butterfish Shell is.

### Features

#### Integrates well with your existing shell, probably

When you run `butterfish shell`, it starts a new instance of your shell, which is probably `bash` or `zsh`. `zsh` is the default on MacOS. If you've customized your shell it shouldn't interfere with that.

Butterfish Shell then sits in front of your shell and does useful things.

It doesn't work with the Windows shell, I probably won't implement that -- for certain technical reasons, that's complicated. I haven't tried `fish` or other more esoteric shells.

#### Start a prompt with a Capital Letter

Within Butterfish Shell you can send a ChatGPT prompt by just starting a command with a capital letter, for example:

```
> How do I do ___?
```

Butterfish Shell is intercepting this and then sending the prompt to ChatGPT.

#### Manages your shell and prompting history

One of the reasons ChatGPT is so useful is you can carry on a conversation. If the last answer wasn't good, you can ask to tweak it.

Butterfish Shell gives you the same capability, but _also_ injects your shell history into the chat. Example:

```
> find *.go
zsh: no matches found: *.go
> Why didn't that work?
It looks like....   # (this is the ChatGPT output)
> Ok, give me a command that will work.
find . -name "*.go"
```

So when you talk with ChatGPT, the past questions/answers and the shell output itself is included in the context.

#### Gives you GPT autosuggest

This is like Github Copilot, but in your terminal shell. Butterfish Shell will autosuggest commands which you can apply with <kbd>Tab</kbd>.

Like prompting, autosuggest context includes your recent history, so if ChatGPT suggested a command to you, it will likely autosuggest that to run next!

#### Transparent and customizable prompts

Most layers on top of ChatGPT add some language around what you type in to help guide the model to give you the right thing. Often you have no idea what's being added.

A goal here is to give you control over that language. To that end, the prompt wrappers are visible and editable: they're kept in `~/.config/butterfish/prompts.yaml`.

#### Select your own model

Butterfish shell defaults to `gpt-3.5-turbo`, but if you have access to `gpt-4` you can use that as well with:

```
> butterfish shell -m gpt-4
```

It also works with the 32k context window GPT-4 model.

#### Goal mode

This is where things get really experimental, wacky, and potentially dangerous. Butterfish Shell has a feature called Goal Mode, which allows ChatGPT to execute commands on it's own to reach a goal. It will give you commands which you execute, and then the results are passed back to ChatGPT. Start a command with `!` to engage this mode. Start a command with `!!` to let it execute commands without confirmation.

```
> !Run pip install in this directory and debug any problems
```

Goal Mode is pretty hit or miss. Good luck!

### Architecture

#### How does Butterfish Shell work?

<img src="https://github.com/bakks/butterfish/raw/plugin/assets/shell.architecture.png" alt="Butterfish" width="500px" />

-   When you run `butterfish shell` in your terminal it starts an instance of your shell (e.g. `/bin/zsh`), then intercepts the shell's input and output. This is why we call it a "shell wrapper".
-   Keyboard input and shell output are buffered in Butterfish's in-memory history.
-   Most input is forwarded directly to the shell, but when you start a prompt with a Capital Letter, that input is kept in Butterfish itself.
-   When you submit a prompt Butterfish will call the OpenAI ChatGPT API, and stream the results back to the terminal (not to the shell).
-   What about when you run a child process like `vim` or `ssh`? Butterfish watches for child processes and avoids interfering when you run an interactive process.
-   Autosuggest functions similarly to prompting - Butterfish will call the OpenAI API and display suggestions in the terminal, then will intercept when you complete it with `Tab`.

#### What does an API request look like?

<img src="https://github.com/bakks/butterfish/raw/plugin/assets/shell.api.example.png" alt="Butterfish" width="400px" />

The [OpenAI ChatGPT API](https://platform.openai.com/docs/api-reference/chat) expects you to submit a "history" of the conversation up to the current prompt. There are 3 kinds of messages in a history:

-   **System Message**: This is a set of instructions that tells the AI how to behave. For Butterfish Shell this is something like `You an an assistant that helps the user with a Unix shell`. You can customize this to whatever you want by editing `~/.config/butterfish/prompts.yaml`.
-   **User Messages**: These are messages that represent the user's input to the history. In Butterfish Shell this includes the commands typed into the shell, the shell's output (i.e. the command output), and also the user's prompts (e.g. asking a question).
-   **Assistant Messages**: These are the AI's past output. If you asked a question and the AI gave you a previous response, this will appear here. That's how the assistant knows what to do when you give a prompt like "I don't like that answer, give me another".

When Butterfish Shell sends a request to ChatGPT it will use its in-memory buffer to construct a history for the ChatGPT API.

But don't forget the API has an important constraint: you can only send it so much data at once! This is the number of "tokens" for a model, GPT-3.5 allows an input/output of up to 4096 tokens at once. A token is a GPT-specific way of splitting text, I think of a token as roughly 1 syllable in a word.

Butterfish Shell will fit as much history into an API request as it can. It roughly follows these rules:

1. Reserve 512 tokens for the answer. The input and output must fit within the model's token window.
1. Add items to the API request history until the rest of the tokens are consumed. This includes previous shell output, shell input, and human prompts.
1. Only use up to 512 tokens for a single history line item. For example, if you printed a huge file to the shell, this will be truncated to 512 tokens.

So remember that the history isn't infinite, it will include as much _recent history_ as possible, old stuff will eventually be outside of the window sent in a request, and very long command outputs will be truncated.

#### The Butterfish Shell <> System Shell interface

In general Butterfish tries to not interfere with your normal shell operation. An exception to this rule is that Butterfish will edit your shell prompt by default. We do this because:

1. We add the 🐠 emoji to your prompt as a signal that you're using Butterfish.
1. We add the previous command's status code to the prompt so that Butterfish knows if it succeeded or not. A 0 means a command was successful, non-0 means it failed.
1. We add the following special characters to the prompt (`\033Q`, `\033R`) so that Butterfish can identify a prompt output. These are privately reserved ANSI terminal escape codes that are pretty uncommon, and so generally won't cause any issues with your workflow.

### Notes on the Butterfish Project

This is an open source project written by Peter Bakkum under the MIT Open Source License.

Project goals:

-   Make the user faster and more effective using LLMs. This should feel fluent, ergonomic, natural.
-   Be unobtrusive, don't break current shell workflows.
-   Don't require another window or using mouse.
-   Use the recent shell history.
-   Be transparent about the exact prompts sent to OpenAI, make these customizable.

This is an experimental tool, I'm eager for feedback. Submit issues, pull requests, etc.!

Above all, I hope you will find this tool useful.
