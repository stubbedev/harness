package model

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/util"
	"github.com/stubbedev/harness/internal/workspace"
)

// submitCheckedMsg carries the readiness check an editor submit waits on.
// The draft stays in the editor until the check passes, so a prompt typed
// while the agent is still starting (or the server is unreachable) is
// reported but not thrown away. forSession and agentView are the session
// and agent view the draft was submitted to: a result that raced a switch
// away from either is dropped, the draft still in the editor.
type submitCheckedMsg struct {
	draft      string
	forSession string
	agentView  string
	err        error
}

// promptReadyMsg carries the readiness check a prompt produced by a
// command (a skill, an MCP prompt, an extension command) waits on.
type promptReadyMsg struct {
	forSession  string
	content     string
	attachments []message.Attachment
	err         error
}

// newSessionCreatedMsg carries the session created for the work started
// while none was open.
type newSessionCreatedMsg struct {
	session session.Session
	err     error
}

// sessionStart is work that waits for the session startInNewSession
// creates: a prompt or a bang-mode shell command.
type sessionStart interface {
	startIn(m *UI) tea.Cmd
}

// promptStart is a prompt waiting for its new session.
type promptStart struct {
	content     string
	attachments []message.Attachment
}

func (s promptStart) startIn(m *UI) tea.Cmd {
	return m.sendReadyMessage(s.content, s.attachments...)
}

// bangStart is a shell command waiting for its new session. The first
// one runs once loadSessionMsg lands, so the chat list is stable before
// its items are added and its output streams.
type bangStart struct {
	command string
}

func (s bangStart) startIn(m *UI) tea.Cmd {
	if m.pendingBangCommand == "" {
		m.pendingBangCommand = s.command
		return nil
	}
	return m.runShellCommandInternal(s.command, false)
}

// submitDraft submits the editor's draft. A command invocation ("/goal
// ...") runs without the agent and goes at once; anything else waits for
// the off-thread readiness check, one at a time, so a repeated enter
// cannot send the draft twice.
func (m *UI) submitDraft(draft string) tea.Cmd {
	if cmd := m.draftRejection(draft); cmd != nil {
		return cmd
	}
	inv, isInvocation := m.editorInvocation(draft)
	if isInvocation && !m.hasInlineAttachments(draft) {
		return m.sendDraft(draft, func(string, []message.Attachment) tea.Cmd { return m.invoke(inv) })
	}
	if m.submitPending {
		return nil
	}
	m.submitPending = true
	ws := m.com.Workspace
	checked := submitCheckedMsg{draft: draft, forSession: m.currentSessionID(), agentView: m.agentView.shown}
	return func() tea.Msg {
		ctx, cancel := common.WorkspaceCallContext()
		defer cancel()
		checked.err = ws.AgentReadyErr(ctx)
		return checked
	}
}

// applySubmitChecked sends the checked draft, unless the user moved to
// another session or agent view, or edited the draft, while the check
// was in flight: the editor then holds what they want, untouched.
func (m *UI) applySubmitChecked(msg submitCheckedMsg) tea.Cmd {
	m.submitPending = false
	if msg.forSession != m.currentSessionID() || msg.agentView != m.agentView.shown ||
		strings.TrimSpace(m.textarea.Value()) != msg.draft {
		return nil
	}
	if msg.err != nil {
		return util.ReportError(msg.err)
	}
	if cmd := m.draftRejection(msg.draft); cmd != nil {
		return cmd
	}
	if m.agentView.shown != "" {
		// Viewing an agent: the prompt steers its session instead of
		// starting a main-session turn. Its message arrives through the
		// normal Created event, so the transcript picks it up without
		// extra plumbing.
		return m.sendDraft(msg.draft, func(value string, _ []message.Attachment) tea.Cmd {
			return tea.Batch(m.steerAgent(value), m.loadPromptHistory())
		})
	}
	return m.sendDraft(msg.draft, func(value string, attachments []message.Attachment) tea.Cmd {
		return tea.Batch(m.sendReadyMessage(value, attachments...), m.loadPromptHistory())
	})
}

// draftRejection reports why the draft cannot be sent as it stands, or
// nil. The draft then stays in the editor rather than being cleared and
// refused.
func (m *UI) draftRejection(draft string) tea.Cmd {
	// An agent's steering channel carries text only, so a prompt with an
	// attachment would lose it.
	if m.agentView.shown != "" && m.hasInlineAttachments(draft) {
		return util.ReportWarn("Attachments can't be sent to a subagent; switch back to the main agent first")
	}
	if err := m.inlineAttachmentsErr(draft); err != nil {
		return util.ReportWarn(err.Error())
	}
	return nil
}

// sendDraft clears the editor and hands the draft to send. Inline
// attachments ride references in the text and are swapped for their
// payloads: paste tokens are stripped, mention text stays. An attachment
// with nothing left to say is a prompt of its own, since the model reads
// an image just as well as text.
func (m *UI) sendDraft(draft string, send func(value string, attachments []message.Attachment) tea.Cmd) tea.Cmd {
	prevHeight := m.textarea.Height()
	m.textarea.Reset()
	heightCmd := m.handleTextareaHeightChange(prevHeight)
	value, attachments := m.resolveInlineAttachments(draft)
	if len(value) == 0 && len(attachments) == 0 {
		return heightCmd
	}
	m.randomizePlaceholders()
	m.historyReset()
	return tea.Batch(heightCmd, send(value, attachments))
}

// sendMessage sends a prompt to the current session once the off-thread
// readiness check passes.
func (m *UI) sendMessage(content string, attachments ...message.Attachment) tea.Cmd {
	ws := m.com.Workspace
	ready := promptReadyMsg{forSession: m.currentSessionID(), content: content, attachments: attachments}
	return func() tea.Msg {
		ctx, cancel := common.WorkspaceCallContext()
		defer cancel()
		ready.err = ws.AgentReadyErr(ctx)
		return ready
	}
}

// applyPromptReady sends a checked prompt to the session it was meant
// for. One that raced a session switch is reported instead of landing in
// a session it was not written for.
func (m *UI) applyPromptReady(msg promptReadyMsg) tea.Cmd {
	if msg.err != nil {
		return util.ReportError(msg.err)
	}
	if msg.forSession != m.currentSessionID() {
		return util.ReportWarn("The prompt was not sent: the session it was meant for is no longer open")
	}
	return m.sendReadyMessage(msg.content, msg.attachments...)
}

// startInNewSession queues work for a session created off-thread. Work
// started while the creation is in flight joins the queue, so it all
// lands in the one session created.
func (m *UI) startInNewSession(start sessionStart) tea.Cmd {
	m.newSessionStarts = append(m.newSessionStarts, start)
	if len(m.newSessionStarts) > 1 {
		return nil
	}
	return createSession(m.com.Workspace)
}

func createSession(ws workspace.Workspace) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := common.WorkspaceCallContext()
		defer cancel()
		created, err := ws.CreateSession(ctx, "New Session")
		if err != nil {
			return newSessionCreatedMsg{err: fmt.Errorf("create session: %w", err)}
		}
		return newSessionCreatedMsg{session: created}
	}
}

// applyNewSessionCreated opens the created session and starts the work
// queued for it there. Should another session have been opened while it
// was being created, the queued work belongs to neither: the prompts go
// back to the editor and the user decides.
func (m *UI) applyNewSessionCreated(msg newSessionCreatedMsg) tea.Cmd {
	starts := m.newSessionStarts
	m.newSessionStarts = nil
	if msg.err != nil {
		return util.ReportError(msg.err)
	}
	if m.hasSession() {
		var prompts []string
		for _, start := range starts {
			if p, ok := start.(promptStart); ok {
				prompts = append(prompts, p.content)
			}
		}
		return tea.Batch(
			util.ReportWarn("Another session was opened while the new one was being created; nothing was sent"),
			m.restorePrompts(prompts),
		)
	}
	m.session = &msg.session
	m.setState(uiChat, m.focus)
	cmds := []tea.Cmd{m.loadSession(msg.session.ID)}
	for _, start := range starts {
		cmds = append(cmds, start.startIn(m))
	}
	return tea.Batch(cmds...)
}
