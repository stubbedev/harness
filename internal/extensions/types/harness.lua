---@meta harness
--- Type definitions for the Harness extension API.
---
--- These annotations are for lua-language-server: they give completion,
--- signature help and diagnostics while writing an extension. Nothing here
--- runs -- the real implementations are host functions registered by
--- Harness when it loads your init.lua.
---
--- Install with `harness extensions types --write`, which drops this file
--- and a matching .luarc.json next to your extensions.

---------------------------------------------------------------------------
-- Tools
---------------------------------------------------------------------------

---A single parameter in the shorthand form of a tool schema.
---@class harness.ParameterSpec
---@field type? "string"|"number"|"integer"|"boolean"|"array"|"object" Defaults to "string".
---@field description? string What the parameter means, as the model reads it.
---@field required? boolean Whether the model must supply it.
---@field enum? string[] Allowed values.
---@field items? table Element schema, for arrays.

---What Harness knows about the call being made, handed to every tool handler.
---@class harness.ToolContext
---@field tool_call_id string The model's ID for this call.
---@field extension string The name of the extension the tool belongs to.
---@field working_dir string The workspace root.
---@field session_id string The session the call belongs to, when one is known.

---The table form of a tool result. A handler may also return a plain string.
---@class harness.ToolResult
---@field content string What the model sees.
---@field is_error? boolean Render this as a failed tool call.
---@field stop_turn? boolean End the turn after this result.
---@field metadata? table Structured data carried alongside the content.

---@class harness.ToolSpec
---@field name string Unique across every tool in the session.
---@field description? string What the tool does, as the model reads it.
---@field parameters? table<string, harness.ParameterSpec|string>|table A shorthand map, or a JSON Schema object with a `properties` key.
---@field parallel? boolean May the model run this alongside other tools?
---@field handler fun(input: table, ctx: harness.ToolContext): string|harness.ToolResult

---------------------------------------------------------------------------
-- Commands
---------------------------------------------------------------------------

---@class harness.CommandArgument
---@field id string The name the palette asks for, and the $NAME placeholder in a static prompt.
---@field title? string Label shown in the palette.
---@field description? string Help text shown under the field.
---@field required? boolean

---@class harness.CommandSpec
---@field name string Shown in the palette as ext:<extension>:<name>.
---@field description? string One line, shown under the command.
---@field prompt? string A static prompt, with $NAME placeholders substituted.
---@field arguments? harness.CommandArgument[]
---@field handler? fun(args: table<string, string>): string Returns the prompt to send. Use instead of `prompt` when the expansion needs code.

---------------------------------------------------------------------------
-- Hooks
---------------------------------------------------------------------------

---The payload a hook handler receives. Which fields are set depends on the
---event; see docs/hooks/README.md.
---@class harness.HookEvent
---@field event string The event name, e.g. "PreToolUse".
---@field session_id string
---@field cwd string
---@field tool_name? string Tool events.
---@field tool_input? table Tool events: the arguments the model sent.
---@field tool_response? table PostToolUse: what the tool returned.
---@field prompt? string UserPromptSubmit.
---@field attachments? string[] UserPromptSubmit.
---@field subagent_type? string SubagentStop.
---@field trigger? string SessionStart, Pre/PostCompact.
---@field notification_type? string Notification.
---@field message? string Notification.

---A handler's verdict. Returning nothing means "no opinion"; returning a
---string adds it to the model's context.
---@class harness.HookResult
---@field decision? "allow"|"deny" Deny blocks the call and shows `reason` to the model.
---@field reason? string
---@field context? string Added to the model's context.
---@field halt? boolean Stop the whole turn.
---@field updated_input? table PreToolUse: a patch shallow-merged over the tool's input.
---@field updated_prompt? string UserPromptSubmit: replaces the user's prompt.

---@alias harness.HookHandler fun(event: harness.HookEvent): harness.HookResult|string|nil

---@alias harness.HookEventName
---| "PreToolUse"
---| "PostToolUse"
---| "UserPromptSubmit"
---| "SessionStart"
---| "Stop"
---| "SubagentStop"
---| "Notification"
---| "PreCompact"
---| "PostCompact"

---@class harness.HookSpec
---@field event harness.HookEventName
---@field matcher? string Regex tested against the event's subject (the tool name, or the sub-agent type for SubagentStop).
---@field handler harness.HookHandler

---------------------------------------------------------------------------
-- Background jobs
---------------------------------------------------------------------------

---@class harness.JobSpec
---@field name string Started by this name with harness.jobs.start.
---@field description? string
---@field timeout? number Seconds. Defaults to 600, capped at 3600.
---@field handler fun(args: table): string|table What it returns waits in the queue until collected.

---@class harness.JobRecord
---@field id string
---@field extension string
---@field name string
---@field state "running"|"done"|"failed"|"canceled"
---@field result string The handler's return value; a table comes back as JSON.
---@field error string
---@field seconds number How long it has run, or did run.

---------------------------------------------------------------------------
-- Host capabilities
---------------------------------------------------------------------------

---@class harness.ExecOptions
---@field cwd? string Relative to the workspace root. Defaults to the root.
---@field stdin? string
---@field async? boolean Return a handle instead of the result.

---@class harness.ExecResult
---@field stdout string
---@field stderr string
---@field code integer The exit status; 0 when the command succeeded.
---@field ok boolean

---@class harness.HttpRequest
---@field url string
---@field method? string Defaults to "GET".
---@field headers? table<string, string>
---@field body? string|table A table is encoded as JSON.
---@field async? boolean Return a handle instead of the response.

---@class harness.HttpResponse
---@field status integer
---@field ok boolean True for 2xx.
---@field body string
---@field headers table<string, string> Lower-cased names.
---@field error? string Set only on an awaited async request that failed to reach the server.

---A pending async call. Await it for the result.
---@class harness.Handle
local Handle = {}

---Waits for this call and returns its result.
---@return harness.ExecResult|harness.HttpResponse
function Handle:await() end

---@class harness.FsEntry
---@field name string
---@field dir boolean

---@class harness.Workspace
---@field root string The workspace root.
---@field data_dir string Where Harness keeps this workspace's state; a good place for an extension's own files.
---@field extension_dir string This extension's own directory.

---------------------------------------------------------------------------
-- The harness table
---------------------------------------------------------------------------

---@class harness
---@field name string This extension's name.
---@field dir string This extension's directory.
---@field version string The Harness version.
harness = {}

---Registers a tool the model can call. Load-time only.
---@param spec harness.ToolSpec
function harness.register_tool(spec) end

---Registers a slash command. Load-time only.
---@param spec harness.CommandSpec
function harness.register_command(spec) end

---Registers a background job handler. Load-time only.
---@param spec harness.JobSpec
function harness.register_job(spec) end

---Registers a hook handler. Load-time only.
---@param event harness.HookEventName|harness.HookSpec
---@param matcher? string|harness.HookHandler A regex, or the handler when no matcher is wanted.
---@param handler? harness.HookHandler
function harness.on(event, matcher, handler) end

---Waits for one or more async handles and returns one result each, in order.
---@param ... harness.Handle
---@return harness.ExecResult|harness.HttpResponse ...
function harness.await(...) end

---Runs a command through the shell Harness embeds.
---@param command string
---@param opts? harness.ExecOptions
---@return harness.ExecResult|harness.Handle
function harness.exec(command, opts) end

---Reads an environment variable.
---@param name string
---@return string|nil
function harness.env(name) end

---Describes the workspace this extension is running in.
---@return harness.Workspace
function harness.workspace() end

harness.log = {}

---@param message string
---@param fields? table
function harness.log.debug(message, fields) end

---@param message string
---@param fields? table
function harness.log.info(message, fields) end

---@param message string
---@param fields? table
function harness.log.warn(message, fields) end

---@param message string
---@param fields? table
function harness.log.error(message, fields) end

harness.json = {}

---@param value any
---@return string
function harness.json.encode(value) end

---@param text string
---@return any
function harness.json.decode(text) end

harness.fs = {}

---Reads a file. Relative paths resolve against the workspace root.
---@param path string
---@return string|nil content, string|nil err
function harness.fs.read(path) end

---Writes a file, creating parent directories.
---@param path string
---@param content string
---@return boolean ok, string|nil err
function harness.fs.write(path, content) end

---Appends to a file, creating it when absent.
---@param path string
---@param content string
---@return boolean ok, string|nil err
function harness.fs.append(path, content) end

---Lists a directory.
---@param path string
---@return harness.FsEntry[]|nil entries, string|nil err
function harness.fs.list(path) end

---@param path string
---@return boolean
function harness.fs.exists(path) end

---@param path string
---@return boolean ok, string|nil err
function harness.fs.mkdir(path) end

harness.http = {}

---@param spec harness.HttpRequest
---@return harness.HttpResponse|harness.Handle|nil response, string|nil err
function harness.http.request(spec) end

---@param url string
---@param headers? table<string, string>
---@return harness.HttpResponse|nil response, string|nil err
function harness.http.get(url, headers) end

---@param url string
---@param body string|table
---@param headers? table<string, string>
---@return harness.HttpResponse|nil response, string|nil err
function harness.http.post(url, body, headers) end

harness.jobs = {}

---Starts a registered job. Returns its ID immediately; the work carries on
---in the background, in a VM of its own.
---@param name string
---@param args? table
---@return string id
function harness.jobs.start(name, args) end

---@param id string
---@return harness.JobRecord|nil
function harness.jobs.status(id) end

---Drains every finished job whose result nothing has collected yet.
---@return harness.JobRecord[]
function harness.jobs.results() end

---@param id string
---@return boolean canceled
function harness.jobs.cancel(id) end

return harness
