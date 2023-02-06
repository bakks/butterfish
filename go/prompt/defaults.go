package prompt

const (
	PromptWatchShellOutput     = "watch_shell_output"
	PromptSummarize            = "summarize"
	PromptSummarizeFacts       = "summarize_facts"
	PromptSummarizeListOfFacts = "summarize_list_of_facts"
	PromptGenerateCommand      = "generate_command"
	PromptQuestion             = "rewrite"
)

var DefaultPrompts []Prompt = []Prompt{
	{
		Name:        PromptWatchShellOutput,
		Prompt:      `The following is output from a user inside a "{shell_name}" shell, the user ran the command "{command}", if the output contains an error then print the specific segment that is an error and explain briefly how to solve the error, otherwise respond with only "NOOP". "{output}"`,
		OkToReplace: true,
	},
	{
		Name: PromptSummarize,
		Prompt: `The following is a raw text file, summarize the file contents, the file's purpose, and write a list of the file's key elements:
'''
{content}
'''

Summary:`,
		OkToReplace: true,
	},
	{
		Name: PromptSummarizeFacts,
		Prompt: `The following is a raw text file, write a bullet-point list of facts from the document starting with the most important.
'''
{content}
'''

Summary:`,
		OkToReplace: true,
	},
	{
		Name: PromptSummarizeListOfFacts,
		Prompt: `The following is a list of facts, write a general description of the document and summarize its important facts in a bulleted list.
'''
{content}
'''

Description and Important Facts:`,
		OkToReplace: true,
	},
	{
		Name: PromptGenerateCommand,
		Prompt: `Write a shell command that accomplishes the following goal. Respond with only the shell command.
'''
{content}
'''

Shell command:`,
		OkToReplace: true,
	},
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
