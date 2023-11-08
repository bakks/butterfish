package prompt

const (
	PromptFixCommand           = "fix_command"
	PromptSummarize            = "summarize"
	PromptSummarizeFacts       = "summarize_facts"
	PromptSummarizeListOfFacts = "summarize_list_of_facts"
	PromptGenerateCommand      = "generate_command"
	PromptQuestion             = "question"
	PromptSystemMessage        = "prompt_system_message"
	ShellAutosuggestCommand    = "shell_autocomplete_command"
	ShellAutosuggestNewCommand = "shell_autocomplete_new_command"
	ShellAutosuggestPrompt     = "shell_autocomplete_prompt"
	ShellSystemMessage         = "shell_system_message"
	GoalModeSystemMessage      = "goal_mode_system_message"
)

// These are the default prompts used for Butterfish, they will be written
// to the prompts.yaml file every time Butterfish is loaded, unless the
// OkToReplace field (in the yaml file) is false.

var DefaultPrompts []Prompt = []Prompt{

	{
		Name:        PromptSystemMessage,
		Prompt:      "You are an assistant that helps the user in a Unix shell. Make your answers technical but succinct.",
		OkToReplace: true,
	},

	{
		Name:        ShellSystemMessage,
		Prompt:      "You are an assistant that helps the user with a Unix shell. Give advice about commands that can be run and examples but keep your answers succinct. You don't need to tell the user how to install commands that you mention. It is ok if the user asks questions not directly related to the unix shell. Here is system info about the local machine: '{sysinfo}'",
		OkToReplace: true,
	},

	{
		Name:        GoalModeSystemMessage,
		Prompt:      "You are an agent helping me achieve the following goal: '{goal}'. You will execute unix commands to achieve the goal. To execute a command, call the command function. Only run one command at a time. I will give you the results of the command. If the command fails, try to edit it or try another command to do the same thing. If we haven't reached our goal, you will then continue execute commands. If there is significant ambiguity then ask me questions. You must verify that the goal is achieved. You must call one of the functions in your response but state your reasoning before calling the function. Here is system info about the local machine: '{sysinfo}'",
		OkToReplace: true,
	},

	{
		Name:        ShellAutosuggestCommand,
		OkToReplace: true,
		Prompt: `You are a unix shell command autocompleter. I will give you the user's history, predict the full command they will type. You will find good suggestions in the user's history. You must suggest a command longer than has been typed thus far.

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

I will give you the user's shell history including assistant messages. Respond with only the prediction, no quotes. This is the start of shell history:
-------------
{history}
> {command}`,
	},

	{
		Name:        ShellAutosuggestNewCommand,
		OkToReplace: true,
		Prompt: `You are a unix shell command predictor. I will give you the user's history, predict a new command they might run. You will find good suggestions in the user's history. The user might have asked a question and you might have suggested a command, if that is recent then suggest that command. Only predict a unix shell command, do not predict output. Provide a single line of text for the response.

Examples of good predictions:
- git status
- ls -l

Start of history:
-------------
{history}
-------------
Predicted command:
`,
	},

	{
		Name:        ShellAutosuggestPrompt,
		OkToReplace: true,
		Prompt: `You are a unix shell question autocompleter. The user has started asking a natural language question, predict the rest of the question. Do not predict an answer to that question. Include the start of the question in your answer.

This is the start of shell history:
-------------
{history}

predicted question:
{command}`,
	},

	// PromptFixCommand is a prompt for fixing a command
	{
		Name:        PromptFixCommand,
		OkToReplace: true,
		Prompt: `The user ran the command "{command}", which failed with exit code {status}. The output from the command is below.
		'''
		{output}
		'''
		We want to do several things:
		1. Explain to the user why the command probably failed. If unsure, explain that you do not know.
		2. Edit the command to fix the problem, don't use placeholders. If unsure, explain that you do not know. If sure, then a new line beginning with '>' and then have the updated command. The final line of your response should only have the updated command.`,
	},

	// PromptSummarize is a prompt for summarizing a command
	{
		Name:        PromptSummarize,
		OkToReplace: true,
		Prompt: `The following is a raw text file, summarize the file contents, the file's purpose, and write a list of the file's key elements:
'''
{content}
'''

Summary:`,
	},

	// PromptSummarizeFacts is a prompt for summarizing facts
	{
		Name:        PromptSummarizeFacts,
		OkToReplace: true,
		Prompt: `The following is a raw text file, write a bullet-point list of facts from the document starting with the most important.
'''
{content}
'''

Summary:`,
	},

	// PromptSummarizeListOfFacts is a prompt for summarizing a list of facts
	{
		Name:        PromptSummarizeListOfFacts,
		OkToReplace: true,
		Prompt: `The following is a list of facts, write a general description of the document and summarize its important facts in a bulleted list.
'''
{content}
'''

Description and Important Facts:`,
	},

	// PromptGenerateCommand is a prompt for generating a command
	{
		Name:        PromptGenerateCommand,
		OkToReplace: true,
		Prompt: `Write a shell command that accomplishes the following goal. Respond with only the shell command.
'''
{content}
'''

Shell command:`,
	},

	// PromptQuestion is a prompt for answering a question
	{
		Name:        PromptQuestion,
		OkToReplace: true,
		Prompt: `Answer this question about files stored on disk. Here are some snippets from the file separated by '---'.
'''
{snippets}
'''
{question}:`,
	},
}
