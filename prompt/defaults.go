package prompt

const (
	PromptWatchShellOutput     = "watch_shell_output"
	PromptFixCommand           = "fix_command"
	PromptSummarize            = "summarize"
	PromptSummarizeFacts       = "summarize_facts"
	PromptSummarizeListOfFacts = "summarize_list_of_facts"
	PromptGenerateCommand      = "generate_command"
	PromptQuestion             = "rewrite"
)

var DefaultPrompts []Prompt = []Prompt{

	// PromptWatchShellOutput is a prompt for watching shell output
	{
		Name:        PromptWatchShellOutput,
		Prompt:      `The following is output from a user inside a "{shell_name}" shell, the user ran the command "{command}", if the output contains an error then print the specific segment that is an error and explain briefly how to solve the error, otherwise respond with only "NOOP". "{output}"`,
		OkToReplace: true,
	},

	// PromptFixCommand is a prompt for fixing a command
	{
		Name: PromptFixCommand,
		Prompt: `The user ran the command "{command}", which failed with exit code {status}. The output from the command is below.
		'''
		{output}
		'''
		We want to do several things:
		1. Explain to the user why the command probably failed. If unsure, explain that you do not know.
		2. Edit the command to fix the problem, don't use placeholders. If unsure, explain that you do not know. If sure, then a new line beginning with '>' and then have the updated command. The final line of your response should only have the updated command.`,
		OkToReplace: true,
	},

	// PromptSummarize is a prompt for summarizing a command
	{
		Name: PromptSummarize,
		Prompt: `The following is a raw text file, summarize the file contents, the file's purpose, and write a list of the file's key elements:
'''
{content}
'''

Summary:`,
		OkToReplace: true,
	},

	// PromptSummarizeFacts is a prompt for summarizing facts
	{
		Name: PromptSummarizeFacts,
		Prompt: `The following is a raw text file, write a bullet-point list of facts from the document starting with the most important.
'''
{content}
'''

Summary:`,
		OkToReplace: true,
	},

	// PromptSummarizeListOfFacts is a prompt for summarizing a list of facts
	{
		Name: PromptSummarizeListOfFacts,
		Prompt: `The following is a list of facts, write a general description of the document and summarize its important facts in a bulleted list.
'''
{content}
'''

Description and Important Facts:`,
		OkToReplace: true,
	},

	// PromptGenerateCommand is a prompt for generating a command
	{
		Name: PromptGenerateCommand,
		Prompt: `Write a shell command that accomplishes the following goal. Respond with only the shell command.
'''
{content}
'''

Shell command:`,
		OkToReplace: true,
	},

	// PromptQuestion is a prompt for answering a question
	{

		Name: PromptQuestion,
		Prompt: `Answer this question about a file:"{filename}". Here are some snippets from the file separated by '---'.
'''
{snippets}
'''
{question}:`,
		OkToReplace: true,
	},
}
