package prompt

const (
	PromptFixCommand                 = "fix_command"
	PromptSummarize                  = "summarize"
	PromptSummarizeFacts             = "summarize_facts"
	PromptSummarizeListOfFacts       = "summarize_list_of_facts"
	PromptGenerateCommand            = "generate_command"
	PromptQuestion                   = "question"
	PromptShellAutosuggestCommand    = "shell_autocomplete_command"
	PromptShellAutosuggestNewCommand = "shell_autocomplete_new_command"
	PromptShellAutosuggestPrompt     = "shell_autocomplete_prompt"
	PromptShellSystemMessage         = "shell_system_message"
	GoalModeSystemMessage            = "goal_mode_system_message"
)

// These are the default prompts used for Butterfish, they will be written
// to the prompts.yaml file every time Butterfish is loaded, unless the
// OkToReplace field (in the yaml file) is false.

var DefaultPrompts []Prompt = []Prompt{

	{
		Name:        PromptShellSystemMessage,
		Prompt:      "You are an assistant that helps the user with a Unix shell. Give advice about commands that can be run and examples but keep your answers succinct.",
		OkToReplace: true,
	},

	{
		Name:        GoalModeSystemMessage,
		Prompt:      "You are an agent helping me achieve the following goal: '{goal}'. You will execute unix commands to achieve the goal. To execute a command, call the command function. Only run one command at a time. I will give you the results of the command. If the command fails, try to edit it or try another command to do the same thing. If we haven't reached our goal, you will then continue execute commands. If there is significant ambiguity then you can ask me questions. You must verify that the goal is achieved based on the output of commands.",
		OkToReplace: true,
	},

	{
		Name:        PromptShellAutosuggestCommand,
		OkToReplace: true,
		Prompt: `The user is asking for an autocomplete suggestion for this Unix shell command, respond with only the suggested command, which should include the original command text, do not add comments or quotations. Here is recent history:
'''
{history}
'''.
If a command was recently suggested by the assistant and it matches the start of the command, suggest that. This is the start of the command: '{command}'.`,
	},

	{
		Name:        PromptShellAutosuggestNewCommand,
		OkToReplace: true,
		Prompt: `The user is using a Unix shell but hasn't yet entered anything. Suggest a unix command based on previous assistant output like an example. If the user has entered a command recently which failed, suggest a fixed version of that command. Respond with only the shell command, do not add comments or quotations. Do not suggest in natural language, suggest as a unix shell command. Here is recent history:
'''
{history}
'''
If a command was recently suggested by the assistant, suggest that.
`,
	},

	{
		Name:        PromptShellAutosuggestPrompt,
		OkToReplace: true,
		Prompt: `The user is asking a natural language question likely related to a unix shell command or to programming. Complete the question and include the start of the question in the answer. Do not answer the question. Respond only with the completion. Here is some recent context and history from the user's shell:
'''
{history}
'''.
This is the start of the question: '{command}'.`,
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
		Prompt: `Answer this question about a file:"{filename}". Here are some snippets from the file separated by '---'.
'''
{snippets}
'''
{question}:`,
	},
}
