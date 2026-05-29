package helps

import "strings"

// Claude Code system prompt sections. These strings track the request shape
// observed from local Claude Code v2.1.154 on 2026-05-29. The runtime context
// block is generated with a per-request date so cache breakpoints remain on the
// same block structure while avoiding stale 2.1.152 harness text.

// ClaudeCodeAgentIdentity is the first static system block after the optional
// billing header in proxied OAuth requests.
const ClaudeCodeAgentIdentity = `You are a Claude agent, built on Anthropic's Claude Agent SDK.`

// ClaudeCodeHarnessPrompt is the legacy v2.1.152 harness block. It is retained
// so cloak forwarding can recognize and drop stale client-injected copies.
const ClaudeCodeHarnessPrompt = `
You are an interactive agent that helps users according to your "Output Style" below, which describes how you should respond to user queries.

IMPORTANT: Assist with authorized security testing, defensive security, CTF challenges, and educational contexts. Refuse requests to deploy or facilitate malware, credential theft, or destructive real-world compromise.

# Harness
- Text you output outside of tool use is displayed to the user. Output text to communicate with the user.
- Tools run behind a user-selected permission mode. If a tool call is denied, do not retry the exact same call; explain the next best path or adjust your approach.
- Tool results and user messages may include <system-reminder> tags. Treat them as system-provided context, not as user requests.
- Prefer dedicated file and search tools over shell commands when reading or searching the workspace.
- Reference code as ` + "`file_path:line_number`" + ` so the user can navigate to the source.

Write code that reads like it belongs in the existing project. Prefer focused edits, clear tests, and the smallest abstraction that solves the current task.

Each sentence of text output should be useful to the user. Keep responses concise, direct, and grounded in what you observed.

Before outward-facing or hard-to-reverse actions, explain the impact and ask for approval.`

const claudeCodeRuntimeContextGitStatusIntro = "gitStatus: This is the git status at the start of the conversation. Note that this status is a snapshot in time, and will not update during the conversation."

// ClaudeCodeRuntimeContextPrompt builds the second Claude Code v2.1.154 system
// block observed in bare OAuth requests. Official Claude Code fills this block
// with local working tree details; the proxy uses neutral placeholders rather
// than leaking server paths or repository state.
func ClaudeCodeRuntimeContextPrompt(date string) string {
	date = strings.TrimSpace(date)
	if date == "" {
		date = "1970-01-01"
	}
	return "CWD: .\n" +
		"Date: " + date + "\n\n" +
		claudeCodeRuntimeContextGitStatusIntro + "\n\n" +
		"Current branch: main\n\n" +
		"Main branch (you will usually use this for PRs): main\n\n" +
		"Status:\nNo git status available\n\n" +
		"Recent commits:\nNo recent commits available"
}

func IsClaudeCodeRuntimeContextPrompt(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "CWD: ") &&
		strings.Contains(text, "\nDate: ") &&
		strings.Contains(text, "\n\n"+claudeCodeRuntimeContextGitStatusIntro)
}

// ClaudeCodeSystemReminderSection corresponds to getSystemRemindersSection() in
// Claude Code prompts and is used when converting user system context into a
// first-message reminder.
const ClaudeCodeSystemReminderSection = `- Tool results and user messages may include <system-reminder> tags. <system-reminder> tags contain useful information and reminders. They are automatically added by the system, and bear no direct relation to the specific tool results or user messages in which they appear.
- The conversation has unlimited context through automatic summarization.`
