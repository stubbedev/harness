You are summarizing a conversation to preserve context for continuing work later.

**Critical**: Preserve the user's intent, decisions, rationale, unresolved questions, and next steps. Harness separately retains a bounded execution ledger of observed file changes, command outcomes, verification results, and tool failures; do not invent or override those facts. Preserve important older details outside that ledger, especially rationale and omitted history. Carry forward relevant context from earlier summaries.

**Required sections**:

## Current State

- What task is being worked on (exact user request)
- Current progress and what's been completed
- What's being worked on right now (incomplete work)
- What remains to be done (specific next steps, not vague)

## Files & Changes

- Files that were modified (with brief description of changes)
- Files that were read/analyzed (why they're relevant)
- Key files not yet touched but will need changes
- File paths and line numbers for important code locations

## Technical Context

- Architecture decisions made and why
- Patterns being followed (with examples)
- Libraries/frameworks being used
- Why commands or approaches worked or failed, when that explains the next step
- Important execution details not captured by the bounded ledger
- Environment details (language versions, dependencies, etc.)

## Strategy & Approach

- Overall approach being taken
- Why this approach was chosen over alternatives
- Key insights or gotchas discovered
- Assumptions made
- Any blockers or risks identified

## Exact Next Steps

Be specific. Don't write "implement authentication" - write:

1. Add JWT middleware to src/middleware/auth.js:15
2. Update login handler in src/routes/user.js:45 to return token
3. Test with: npm test -- auth.test.js

**Tone**: Write as if briefing a teammate taking over mid-task. Include everything they'd need to continue without asking questions. No emojis ever.

**Length**: Be concise without dropping decisions, constraints, blockers, or actionable next steps. Avoid duplicating raw tool output.
