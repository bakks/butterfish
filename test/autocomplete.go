package main

// This file is a script to experiment with strategies for autosuggest, like
// different prompts or using functions

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/mitchellh/go-homedir"
	"github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema"
)

func getToken() string {
	path, err := homedir.Expand("~/.config/butterfish/butterfish.env")
	if err != nil {
		log.Fatal(err)
	}

	// We attempt to get a token from env vars plus an env file
	godotenv.Load(path)
	token := os.Getenv("OPENAI_TOKEN")
	return token
}

var client *openai.Client

var COMPLETE_FUNC = []openai.FunctionDefinition{
	{
		Name:        "completecommand",
		Description: "Give the user a completion for a command, the user has started typing the command, guess the best full command. For example if the user has typed 'l' you might complete it as 'ls'.",
		Parameters: jsonschema.Definition{
			Type: jsonschema.Object,
			Properties: map[string]jsonschema.Definition{
				"cmd": {
					Type:        jsonschema.String,
					Description: "The full unix command, for example: ls ~",
				},
			},
			Required: []string{"cmd"},
		},
	},
}

type args struct {
	Cmd string `json:"cmd"`
}

// given a json string, parse as an args struct and return the cmd field
func parseFunctionArguments(argString string) string {
	var args args
	err := json.Unmarshal([]byte(argString), &args)
	if err != nil {
		log.Fatal(err)
	}
	return args.Cmd
}

type testCase struct {
	History  []openai.ChatCompletionMessage
	Expected string
}

var TEST_CASES []testCase = []testCase{
	{
		Expected: "find . -name \"*.go\"",
		History: []openai.ChatCompletionMessage{
			{
				Role: "user",
				Content: `> ls *.go
main.go
foo.go
`,
			},
			{
				Role:    "user",
				Content: `How do I find go files recursively?`,
			},
			{
				Role: "assistant",
				Content: `You can use the find command to find files recursively.

"""
find . -name "*.go"
"""
`,
			},
			{
				Role:    "user",
				Content: `> fi`,
			},
		},
	},
	{
		Expected: "find . -name \"*.go\"",
		History: []openai.ChatCompletionMessage{
			{
				Role: "user",
				Content: `> ls *.go
main.go
foo.go
`,
			},
			{
				Role:    "user",
				Content: `How do I find go files recursively?`,
			},
			{
				Role: "assistant",
				Content: `You can use the find command to find files recursively.

"""
find . -name "*.go"
"""
`,
			},
			{
				Role:    "user",
				Content: `> find . -n`,
			},
		},
	},
	{
		Expected: "git status",
		History: []openai.ChatCompletionMessage{
			{
				Role: "user",
				Content: `> ls *.go
main.go
foo.go
`,
			},
			{
				Role:    "user",
				Content: `> git s`,
			},
		},
	},
	{
		Expected: "git status",
		History: []openai.ChatCompletionMessage{
			{
				Role: "user",
				Content: `> ls *.go
main.go
foo.go
`,
			},
			{
				Role:    "user",
				Content: `> git stat`,
			},
		},
	},
}

type configuration struct {
	Name        string
	Temperature float32
	Prompt      string
	Sysmsg      string
	Model       string
	Functions   []openai.FunctionDefinition
}

var configs = []configuration{
	{
		Name:        "instruct-1",
		Temperature: 0.2,
		Model:       "gpt-3.5-turbo-instruct",
		Prompt: `The user is typing a unix shell command, complete the command. Here is recent history from the user's shell:
'''
{history}
'''
If a command appears recently in history and it matches the start of the command, suggest that. Complete this command, respond with only the completion, no quotes:`,
	},
	{
		Name:        "instruct-2",
		Temperature: 0.2,
		Model:       "gpt-3.5-turbo-instruct",
		Prompt: `The user is typing a unix shell command, complete the command. Complete this command, respond with only the completion, no quotes:
{history}`,
	},
	{
		Name:        "instruct-3",
		Temperature: 0.2,
		Model:       "gpt-3.5-turbo-instruct",
		Prompt: `The user is typing a unix shell command, complete the command. Complete this command, respond with only the completion, no quotes:
{history}`,
	},
	{
		Name:        "instruct-4",
		Temperature: 0.2,
		Model:       "gpt-3.5-turbo-instruct",
		Prompt: `The user is typing a unix shell command, complete the command. If a command appears recently in history and it matches the start of the command, suggest that. Complete this command, respond with only the completion, no quotes:
{history}`,
	},
	{
		Name:        "instruct-5",
		Temperature: 0.2,
		Model:       "gpt-3.5-turbo-instruct",
		Prompt: `The user is typing a unix shell command, complete the command. If a command appears recently in history and it matches the start of the command, suggest that.
Here are examples of prompts and desired completions:
prompt: > fi
completion: find
prompt: > l
completion: ls
prompt: > find . -name 
completion: find . -name '*'

Complete this command, respond with only the completion, no quotes:
{history}`,
	},
	{
		Name:        "instruct-6",
		Temperature: 0.2,
		Model:       "gpt-3.5-turbo-instruct",
		Prompt: `The user is typing a unix shell command, predict the command. If a command appears recently in history and it matches the start of the command, suggest that.
Here are examples of prompts and predictions:

prompt: > fi
prediction: find
prompt: > l
prediction: ls
prompt: > find . -name 
prediction: find . -name '*'

Predict the full command, respond with only the prediction, no quotes:
{history}`,
	},
	{
		Name:        "instruct-7",
		Temperature: 0.1,
		Model:       "gpt-3.5-turbo-instruct",
		Prompt: `You are a unix shell command autocompleter. I will give you the user's history, predict the full command they will type. You will find good suggestions in the user's history, suggest the full command.

Here are examples of prompts and predictions:

prompt: > tel
prediction: telnet

prompt: > l
prediction: ls

prompt: > git a
prediction: git add *

prompt: How do I do a recursive find? """ find . -name "*.go" """ > fin
prediction: find . -name "*.go"

prompt: How do I do a recursive find? """ find . -name "*.go" """ > find .
prediction: find . -name "*.go"

I will give you the user's shell history including assistant messages. Predict the full command, respond with only the prediction, no quotes. This is the start of shell history:
-------------
{history}`,
	},
	{
		Name:        "function-1",
		Temperature: 0.2,
		Model:       "gpt-3.5-turbo",
		Sysmsg:      "You are a helpful assistant on the Unix shell. You are helping the user type a command. Call the completecommand function with the completion.",
		Functions:   COMPLETE_FUNC,
	},
}

// these are iso terminal colors
const (
	RED   = "\033[31m"
	GREEN = "\033[32m"
	BLUE  = "\033[34m"
	WHITE = "\033[37m"
)

type testContext struct {
	Client   *openai.Client
	NumTries int
}

func main() {
	token := getToken()
	if token == "" {
		log.Println("OPENAI_TOKEN is not set")
		os.Exit(1)
	}

	// Create a client
	client = openai.NewClient(token)
	testContext := testContext{client, 5}

	for _, config := range configs {
		fmt.Printf("Running %s\n", config.Name)
		fullScore := 0.0

		for _, test := range TEST_CASES {
			score := runTestCase(testContext, config, test)
			fullScore += score
		}

		fmt.Printf("%s%s: %f%s\n\n", BLUE, config.Name, fullScore, WHITE)
	}
}

func getHistoryStr(test testCase) (string, string) {
	var historyStr string

	for _, msg := range test.History[:len(test.History)-1] {
		historyStr += msg.Content + "\n"
	}

	return historyStr, test.History[len(test.History)-1].Content
}

func runTestCase(ctx testContext, config configuration, test testCase) float64 {
	var results []string

	if config.Sysmsg != "" {
		// chat completion test
		sysmsg := openai.ChatCompletionMessage{
			Role:    "system",
			Content: config.Sysmsg,
		}
		messages := append([]openai.ChatCompletionMessage{sysmsg}, test.History...)
		results = chatCompletion(
			messages,
			config.Functions,
			config.Model,
			config.Temperature,
			ctx.NumTries)

	} else {
		// instruct test
		historyStr, toComplete := getHistoryStr(test)
		prompt := strings.ReplaceAll(config.Prompt, "{history}", historyStr)
		prompt += "\n" + toComplete
		results = completion(prompt, config.Model, config.Temperature, ctx.NumTries)
	}

	var score float64

	for _, result := range results {
		if result == test.Expected {
			score += 1
			fmt.Printf("%s", GREEN)
		} else {
			fmt.Printf("%s", RED)
		}
		fmt.Printf("comparison:  %s  %s\n", result, test.Expected)
	}
	fmt.Printf("%s", WHITE)
	score /= float64(ctx.NumTries)

	return score
}

func completion(prompt string, model string, temp float32, n int) []string {
	req := openai.CompletionRequest{
		Model:       model,
		MaxTokens:   128,
		Temperature: temp,
		N:           n,
		Prompt:      prompt,
	}

	//fmt.Printf("prompt: %s\n", prompt)
	resp, err := client.CreateCompletion(context.Background(), req)
	if err != nil {
		log.Fatal(err)
	}

	var results []string = make([]string, len(resp.Choices))
	for i, choice := range resp.Choices {
		results[i] = strings.TrimSpace(choice.Text)
	}

	return results
}

func chatCompletion(messages []openai.ChatCompletionMessage, functions []openai.FunctionDefinition, model string, temp float32, n int) []string {
	req := openai.ChatCompletionRequest{
		Model:       model,
		MaxTokens:   128,
		Temperature: temp,
		N:           n,
		Messages:    messages,
		Functions:   functions,
	}

	//fmt.Printf("messages: %v\n", messages)

	resp, err := client.CreateChatCompletion(context.Background(), req)
	if err != nil {
		log.Fatal(err)
	}

	var results []string = make([]string, len(resp.Choices))
	for i, choice := range resp.Choices {
		cmd := parseFunctionArguments(choice.Message.FunctionCall.Arguments)
		results[i] = strings.TrimSpace(cmd)
	}

	return results
}
