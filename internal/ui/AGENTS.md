| `chat/search.go`      | Glob, Grep, LS, Web Search                     |# UI Development Instructions

## General Guidelines

- Never use commands to send messages when you can directly mutate children
  or state.
- Keep things simple; do not overcomplicate.
- Create files if needed to separate logic; do not nest models.
- Never do IO or expensive work in `Update`; always use a `tea.Cmd`.
- Never change the model state inside of a command. Use messages and update
  the state in the main `Update` loop.
- Use the `github.com/charmbracelet/x/ansi` package for any string
  manipulation that might involve ANSI codes. Do not manipulate ANSI strings
  at byte level! Some useful functions:
  - `ansi.Cut`
  - `ansi.StringWidth`
  - `ansi.Strip`
  - `ansi.Truncate`

## Architecture

### Rendering Pipeline

The UI uses a **hybrid rendering** approach:

1. **Screen-based (Ultraviolet)**: The top-level `UI` model creates a
   `uv.ScreenBuffer`, and components draw into sub-regions using
   `uv.NewStyledString(str).Draw(scr, rect)`. Layout is rectangle-based via
   a `uiLayout` struct with fields like `layout.header`, `layout.main`,
   `layout.editor`, `layout.tasks`, `layout.pills`, `layout.status`.
2. **String-based**: Sub-components like `list.List` and `completions` render
   to strings, which are painted onto the screen buffer.
3. **`View()`** creates the screen buffer, calls `Draw()`, then
   `canvas.Render()` flattens it to a string for Bubble Tea.

### Main Model (`model/ui.go`)

The `UI` struct is the top-level Bubble Tea model. Key fields:

- `width`, `height` — terminal dimensions
- `layout uiLayout` — computed layout rectangles
- `state uiState` — `uiOnboarding | uiLanding | uiChat`
- `focus uiFocusState` — `uiFocusNone | uiFocusEditor | uiFocusMain`
- `chat *Chat` — wraps `list.List` for the message view
- `textarea textarea.Model` — the input editor
- `dialog *dialog.Overlay` — stacked dialog system
- `completions` — sub-components

Keep most logic and state here. This is where:

- Message routing happens (giant `switch msg.(type)` in `Update`)
- Focus and UI state is managed
- Layout calculations are performed
- Dialogs are orchestrated

### Centralized Message Handling

The `UI` model is the **sole Bubble Tea model**. Sub-components (`Chat`,
`List`, `Completions`, etc.) do not participate in the
standard Elm architecture message loop. They are stateful structs with
imperative methods that the main model calls directly:

- **`Chat`** and **`List`** have no `Update` method at all. The main model
  calls targeted methods like `HandleMouseDown()`, `ScrollBy()`,
  `SetMessages()`, `Animate()`.
- **`Completions`** has a non-standard `Update` signature (returning
  `bool` for "consumed") that acts as a guard, not as a full Bubble Tea
  model.
- **Background tasks strip** (subagents) is not its own model: it renders
  from `m.agentTasks` in `model/tasks.go`.

When writing new components, follow this pattern:

- Expose imperative methods for state changes (not `Update(tea.Msg)`).
- Return `tea.Cmd` from methods when side effects are needed.
- Handle rendering via `Render(width int) string` or
  `Draw(scr uv.Screen, area uv.Rectangle)`.
- Let the main `UI.Update()` decide when and how to call into the component.

### Chat View (`model/chat.go`)

The `Chat` struct wraps a `list.List` with an ID-to-index map, mouse
tracking (drag, double/triple click), animation management, and a `follow`
flag for auto-scroll. It bridges screen-based and string-based rendering:

```go
func (m *Chat) Draw(scr uv.Screen, area uv.Rectangle) {
    uv.NewStyledString(m.list.Render()).Draw(scr, area)
}
```

Individual chat items in `chat/` should be simple renderers that cache their
output and invalidate when data changes (see `cachedMessageItem` in
`chat/messages.go`).

## Key Patterns

### Composition Over Inheritance

Use struct embedding for shared behaviors. See `chat/messages.go` for
examples of reusable embedded structs for highlighting, caching, and focus.

### Interface Hierarchy

The chat message system uses layered interface composition:

- **`list.Item`** — base: `Render(width int) string`
- **`MessageItem`** — extends `list.Item` + `list.RawRenderable` +
  `Identifiable`
- **`ToolMessageItem`** — extends `MessageItem` with tool call/result/status
  methods
- **Opt-in capabilities**: `Focusable`, `Highlightable`, `Expandable`,
  `Animatable`, `Compactable`, `KeyEventHandler`

Key interface locations:

- List item interfaces: `list/item.go`
- Chat message interfaces: `chat/messages.go`
- Tool message interfaces: `chat/tools.go`
- Dialog interface: `dialog/dialog.go`

### Tool Renderers

Each tool has a dedicated renderer in `chat/`. The `ToolRenderer` interface
requires:

```go
RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string
```

`NewToolMessageItem` in `chat/tools.go` is the central factory that routes
tool names to specific types:

| File                  | Tools rendered                                 |
| --------------------- | ---------------------------------------------- |
| `chat/shell_tool.go`  | Shell                                          |
| `chat/file.go`        | View, Write, Edit                              |
| `chat/search.go`      | Glob, Grep, LS, Web Search                     |
| `chat/fetch.go`       | Fetch, WebFetch, WebSearch                     |
| `chat/toolgroup.go`   | Collapsed groups of consecutive tool calls     |
| `chat/diagnostics.go` | Diagnostics                                    |
| `chat/references.go`  | References                                     |
| `chat/lsp_restart.go` | LSPRestart                                     |
| `chat/mcp.go`         | MCP tools (`mcp_` prefix)                      |
| `chat/generic.go`     | Fallback for unrecognized tools                |
| `chat/assistant.go`   | Assistant messages (thinking, content, errors) |
| `chat/user.go`        | User messages (input + attachments)            |

Consecutive tool calls are folded into one `ToolGroupMessageItem`
(`chat/toolgroup.go`): a collapsed "Ran (N tool calls)" row that expands
first to one-liners and then to the calls' full renderers. Children are
never list items of their own — `Chat.ToolItem` resolves through groups.
Subagent dispatches (`agent`, `research`) never render in the transcript;
they live in the background tasks strip (`model/tasks.go`) between the
chat and the pills.

### Styling

- All styles are defined in `styles/styles.go` (massive `Styles` struct with
  nested groups for Header, Pills, Dialog, Help, etc.).
- Access styles via `*common.Common` passed to components.
- Use semantic color fields rather than hardcoded colors.

### Dialogs

- Implement the `Dialog` interface in `dialog/dialog.go`:
  `ID()`, `HandleMsg()` returning an `Action`, `Draw()` onto `uv.Screen`.
- `Overlay` manages a stack of dialogs with push/pop/contains operations.
- Dialogs draw last and overlay everything else.
- Position via `DrawCenterCursor`/`DrawCenter` — never center by hand. They
  anchor horizontally centered, vertically per `options.tui.dialog_placement`:
  bottom edge (which-key style, the default) or top edge (noice.nvim style).
- Use `RenderContext` from `dialog/common.go` for consistent layout (title
  gradients, width, gap, cursor offset helpers).

#### Dialog rendering rules

These prevent the wrapping/overflow bugs that recur whenever a new dialog
is copy-pasted from an old one. In lipgloss v2 `Width(n)` is the **total**
box width — border and padding live *inside* it.

- Size content to the dialog's **content area**, not the outer width:
  `innerWidth := m.width - t.Dialog.View.GetHorizontalFrameSize()`. Sizing a
  block to the full `m.width` makes it 1–2 cols too wide, so the dialog
  frame re-wraps it (the classic "last few chars wrap" bug).
- Inset text with **`Padding`, never `Margin`**. Margin sits outside the
  width and pushes the block past the frame; padding is inside the width
  and applies to every wrapped line.
- Render styled text segments **individually** and concatenate the results
  (`styleA.Render(x) + styleB.Render(y)`), rather than concatenating raw
  strings and wrapping the whole thing in one style. An inner segment's
  reset code drops the outer color for everything after it.
- Use the shared helpers instead of re-deriving widths:
  - text inputs → `dialogInputTextWidth(t, input, innerWidth)` (accounts
    for the `"> "` prompt);
  - titles → `common.DialogTitle` (truncates instead of wrapping);
  - list + scrollbar → `joinScrollbar`;
  - hiding a crowded info column → `applyInfoColumnVisibility`.

### Keybind hints

The status bar's bottom row is the single hint surface: it shows the
help of whatever owns the keyboard (front dialog, inline editor, or the
main view) via the `help.KeyMap` implementations. Dialogs must not
render their own hint row, and rendered labels (header, pills, palette
shortcuts) must come from `binding.Help().Key`, never a hardcoded key
string, so an `options.tui.keybinds` rebind moves every hint. Hints must
be gated on the state where the key is actually routed — a binding shown
where it is inert is a bug. The keys package tests enforce that every
binding carries help text, so any hint is always renderable.
- Clamp width/height to the drawable `area` (`max(0, min(maxW, area.Dx()-frame))`)
  so dialogs stay inside small terminals.

### Shared Context

The `common.Common` struct holds `*app.App` and `*styles.Styles`. Thread it
through all components that need access to app state or styles.

## File Organization

- `model/` — Main UI model and major sub-models (chat, background tasks, header,
  status, pills, session, onboarding, keys, etc.)
- `chat/` — Chat message item types and tool renderers
- `dialog/` — Dialog implementations (models, sessions, commands,
  permissions, API key, OAuth, filepicker, reasoning, quit)
- `list/` — Generic lazy-rendered scrollable list with viewport tracking
- `common/` — Shared `Common` struct, layout helpers, markdown rendering,
  diff rendering, scrollbar
- `completions/` — Autocomplete popup with filterable list
- `styles/` — All style definitions, color tokens, icons
- `diffview/` — Unified and split diff rendering with syntax highlighting
- `anim/` — Animated spinnner
- `logo/` — Logo rendering
- `util/` — Small shared utilities and message types

## Common Gotchas

- Always account for padding/borders in width calculations.
- Use `tea.Batch()` when returning multiple commands.
- Pass `*common.Common` to components that need styles or app access.
- When writing tea.Cmd's prefer creating methods in the model instead of writing inline functions.
- The `list.List` only renders visible items (lazy). No render cache exists
  at the list level — items should cache internally if rendering is
  expensive.
- Rendering is the chat's hot path; a few invariants keep resize/scroll fast
  on large conversations:
  - Syntax highlighting and diff formatting build the chroma style from the
    theme, which is expensive — it is memoized in `common.ChromaStyle`, and
    lexer lookups in `xchroma.MatchLexer`. Don't call
    `chroma.MustNewStyle` / `lexers.Match` directly on a render path.
  - `list.TotalHeight` renders **every** item; it's only for exact scrollbar
    geometry. For "does it overflow?" use the bounded `list.Overflows`. Never
    call `TotalHeight` per frame during a resize — the chat suppresses the
    scrollbar mid-drag and warms the cache incrementally (`list.Prewarm`)
    on settle instead.
- Dialog messages are intercepted first in `Update` before other routing.
- Focus state determines key event routing: `uiFocusEditor` sends keys to
  the textarea, `uiFocusMain` sends them to the chat list.

## Styling System

The styling system lives in `internal/ui/styles/` and is organized into
three layers:

- **`quickstyle.go`**: The stable base theme builder. `quickStyle(opts)`
  constructs a `Styles` struct from `quickStyleOpts` — a palette of
  design tokens (primary, secondary, fgBase, bgBase, success, error, etc.).
  `quickStyle` must be fully token-driven: never hardcode specific
  `charmtone.*` colors here (except Chroma syntax highlighting, which is
  pending tokenization). This lets any theme reuse the base without
  inheriting Charmtone-specific colors.
- **`themes.go`**: Defines concrete themes. Each theme function (e.g.
  `CharmtonePantera`) calls `quickStyle` with its palette, then applies
  theme-specific overrides as needed.
- **`styles.go`**: Defines the `Styles` struct and its documentation —
  the shape of what `quickStyle` produces.

**Adding theme-specific overrides**: When a style genuinely needs a
color that doesn't fit the token model (e.g. the bang prompt uses
Salt/Hazy/Larple), keep `quickStyle` on the closest semantic token and
override only the differing colors in the theme function:

```go
func CharmtonePantera() Styles {
	s := quickStyle(quickStyleOpts{ /* palette */ })

	// Override only the colors that differ from the token defaults.
	s.Editor.PromptBangIconFocused = s.Editor.PromptBangIconFocused.
		Foreground(charmtone.Salt).
		Background(charmtone.Hazy)

	return s
}
```

**Adding a new theme**: Add a palette function in `themes.go` that
returns a `quickStyleOpts` (plus an overrides function when the theme
needs colors outside the token model), then register both in the
`builtinThemes` / `builtinThemeOverrides` maps. Users select the theme
via `options.tui.theme`, or the theme picker (ctrl+shift+t) which writes it; a
configured theme wins over the provider-based `ThemeForProvider`
mapping.
