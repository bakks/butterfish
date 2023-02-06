package prompt

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	yaml "gopkg.in/yaml.v2"
)

// Prompt struct with fields Name, Prompt string, OkToReplace bool
type Prompt struct {
	Name        string
	Prompt      string
	OkToReplace bool
}

// PromptLibrary struct which includes a Path string and a Prompts instance
type PromptLibrary struct {
	Path          string
	Prompts       []Prompt
	Verbose       bool
	VerboseWriter io.Writer
}

// NewPromptLibrary function to make a NewPromptLibrary which takes a path argument
func NewPromptLibrary(path string) *PromptLibrary {
	return &PromptLibrary{
		Path: path,
	}
}

// Returns a list of fields to interpolate (strings wrapped in { and })
func getFields(prompt string) []string {
	// regex to find all fields in a string
	regex := regexp.MustCompile(`\{[a-zA-Z0-9_]+\}`)
	return regex.FindAllString(prompt, -1)
}

// Fetch a prompt with a given name, interpolating the fields into the prompt string.
// Throws an error if fields are missing.
// The argument pattern is first the field name, then the value, for example:
//   GetPrompt("my_prompt", "name", "John", "age", "30")
func (this *PromptLibrary) GetPrompt(name string, args ...string) (string, error) {

	// first find the prompt given the name
	index := this.ContainsPromptNamed(name)
	if index == -1 {
		return "", errors.New("Prompt not found")
	}
	prompt := this.Prompts[index]
	fields := getFields(prompt.Prompt)

	// check that the number of fields matches the number of arguments
	if len(fields)*2 != len(args) {
		fieldNames := strings.Join(fields, ", ")
		return "", fmt.Errorf("Incorrect number of fields provided, prompt %s requires fields (%s)", prompt.Name, fieldNames)
	}

	// turn args into a map
	argMap := make(map[string]string)
	for i := 0; i < len(args); i += 2 {
		argMap[args[i]] = args[i+1]
	}

	promptString := prompt.Prompt

	// interpolate fields using the argMap
	for _, field := range fields {
		fieldName := field[1 : len(field)-1] // trim { and } from field
		value, ok := argMap[fieldName]
		if !ok {
			fieldNames := strings.Join(fields, ", ")
			return "", fmt.Errorf("Missing field %s, prompt %s requires fields (%s)", field, name, fieldNames)
		}
		promptString = strings.Replace(promptString, field, value, -1)
	}

	return promptString, nil
}

// Write a yaml file at the path with the contents marshalled from Prompts
func (this *PromptLibrary) Save() error {
	if this.Prompts == nil || len(this.Prompts) == 0 {
		return errors.New("No prompts to write, please initialize the prompt library")
	}
	bytes, err := yaml.Marshal(this.Prompts)
	if err != nil {
		return errors.New("There was a problem marshalling prompt library, please ensure you are passing in a vaild PromptLibrary struct.")
	}

	// create any directories necessary to write the file
	err = os.MkdirAll(filepath.Dir(this.Path), 0755)
	if err != nil {
		return errors.New("Unable to access directory, please check write permissions and try again.")
	}

	err = ioutil.WriteFile(this.Path, bytes, 0644)
	if err != nil {
		return errors.New("Unable to write file, please check write permissions and try again.")
	}
	return nil
}

func (this *PromptLibrary) ContainsPromptNamed(name string) int {
	for i, prompt := range this.Prompts {
		if prompt.Name == name {
			return i
		}
	}
	return -1
}

// Given an array of Prompt objects, replace prompts in the prompt library based on name, only if OkToReplace is true on the Prompt already in the library
func (this *PromptLibrary) ReplacePrompts(newPrompts []Prompt) {
	for _, newPrompt := range newPrompts {
		index := this.ContainsPromptNamed(newPrompt.Name)
		if index == -1 {
			this.Prompts = append(this.Prompts, newPrompt)
		} else {
			this.Prompts[index] = newPrompt
		}
	}
}

// Check if the library file exists, should be called before Load()
func (this *PromptLibrary) LibraryFileExists() bool {
	if _, err := os.Stat(this.Path); os.IsNotExist(err) {
		return false
	}
	return true
}

// Load a yaml file at the path with a contents marshalled into Prompts
func (this *PromptLibrary) Load() error {
	data, err := ioutil.ReadFile(this.Path)
	if err != nil {
		return errors.New("Unable to access prompt file, please check write permissions and try again.")
	}
	err = yaml.Unmarshal(data, &this.Prompts)
	if err != nil {
		return errors.New("File is not formatted correctly. Please ensure you are passing in a valid YAML file and try again.")
	}

	if this.Verbose {
		fmt.Fprintf(this.VerboseWriter, "Loaded %v prompts from %v\n", len(this.Prompts), this.Path)
	}
	return nil
}
