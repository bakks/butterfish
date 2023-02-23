# Butterfish Prompt Library

A goal of Butterfish is to make prompts transparent and easily editable. Butterfish will write a prompt library to `~/.config/butterfish/prompts.yaml` and load this every time it runs. You can edit prompts in that file to tweak them. If you edit a prompt then set `OkToReplace: false`, which prevents overwriting.

This is the golang module for managing a library of LLM prompts in a YAML file on disk, then loading and interpolating the prompts at runtime. This module can be used separately from Butterfish itself.

Why do we have the `OkToReplace` field to prevent overwriting specific prompts on disk? Because we want a library of default prompts (found at `./defaults.go` in this directory) and we want to be able to update the defaults when a new version of Butterfish is released, but prevent overwritting prompts that the user has customized.

A prompt consists of a string name, the prompt itself, and a field indicating whether or not a prompt can be overwritten. When written to the YAML file, these look like the following.

```yaml
> head -n 8 ~/.config/butterfish/prompts.yaml
- name: watch_shell_output
  prompt: The following is output from a user inside a "{shell_name}" shell, the user
    ran the command "{command}", if the output contains an error then print the specific
    segment that is an error and explain briefly how to solve the error, otherwise
    respond with only "NOOP". "{output}"
  oktoreplace: true
```

To fetch and interpolate that prompt at runtime (after initializing the prompt library) you would make a call like:

```Go
prompt, err := this.PromptLibrary.GetPrompt("watch_shell_output",
  "shell_name", openCmd,
  "command", lastCmd,
  "output", string(output))
```

The `GetPrompt()` method will throw an error if the expected fields are missing.

Here's a more full lifecycle example that demonstrates creating/initializing the prompt library.

```
import "fmt"
import "os"
import "github.com/bakks/butterfish/prompt"


func NewDiskPromptLibrary(path string, verbose bool, writer io.Writer) (*prompt.DiskPromptLibrary, error) {
	promptLibrary := prompt.NewPromptLibrary(path, verbose, writer)
	loaded := false

	if promptLibrary.LibraryFileExists() {
		err := promptLibrary.Load()
		if err != nil {
			return nil, err
		}
		loaded = true
	}
	promptLibrary.ReplacePrompts(prompt.DefaultPrompts)
	promptLibrary.Save()

	if !loaded {
		fmt.Fprintf(writer, "Wrote prompt library at %s\n", path)
	}

	return promptLibrary, nil
}

func main() {
  library, err := NewDiskPromptLibrary("./prompts.yaml", true, os.Stdout)
  if err != nil {
    panic(err)
  }

  prompt, err := this.PromptLibrary.GetPrompt("watch_shell_output",
    "shell_name", openCmd,
    "command", lastCmd,
    "output", string(output))

  if err != nil {
    panic(err)
  }

  fmt.Printf("%s\n", prompt)
}
```
