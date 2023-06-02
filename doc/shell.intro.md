# What is Butterfish Shell?

## The Short Answer

An open-source way to embed OpenAI ChatGPT in your terminal shell without having to invoke it every time. It's easily accessible and can see your shell history.

| MacOS                                                     | Linux/MacOS                                                                                             |
| --------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| `brew install bakks/bakks/butterfish && butterfish shell` | `go install github.com/bakks/butterfish/cmd/butterfish@latest && $(go env GOPATH)/bin/butterfish shell` |

Once you're in Butterfish shell, talk to ChatGPT by starting a command with a _Capital Letter_. This uses your own OpenAI account directly.

## The Long Answer

If you work with software you likely spend _some_ time in a terminal shell. The terminal is a direct window into your machine, and feels both powerful and tedious. You can do almost anything directly from the command line, as long as you correctly summon arcane unix commands.

I've been experimenting with getting LLMs (like ChatGPT) to help you with the command line, for example to suggest a command or summarize a file. But it feels clunky run something like `llm 'answer this question'` every time. And it should be able to see the history, I don't want to copy/paste stuff back and forth!

Here's my solution: put a transparent layer around the terminal shell that communicates with ChatGPT. You can prompt ChatGPT at any time, the anwsers include the right context, and you can do autosuggest at this layer, among other things. This feels like the right way to do a ChatGPT CLI: the option to talk to ChatGPT is unobtrusively always there, when I do it I don't need to copy over stuff from the terminal, and when I get an answer I don't need to copy stuff back. This feels like magic.

I'm using Butterfish Shell for myself constantly, it might be useful for you so please try it. You'll notice the Butterfish project has a bunch of specific CLI commands, but lately I've been focused instead on the shell mode. This document is here to give you a mental model of what and why Butterfish Shell is.

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

**Transparent and customizable prompts**
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

This mode is pretty hit or miss. Good luck!
