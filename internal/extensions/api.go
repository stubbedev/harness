package extensions

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"time"

	"github.com/stubbedev/harness/internal/hooks"
	"github.com/stubbedev/harness/internal/version"
	lua "github.com/yuin/gopher-lua"
)

// toolSpec is a tool an extension registered while loading.
type toolSpec struct {
	name        string
	description string
	parameters  map[string]any
	required    []string
	parallel    bool
	fn          *lua.LFunction
	in          *instance
}

// commandSpec is a slash command an extension registered while loading.
// Exactly one of prompt and fn carries the expansion: a static prompt is
// answered from the listing, a handler is called when the command runs.
type commandSpec struct {
	name        string
	description string
	prompt      string
	arguments   []Argument
	fn          *lua.LFunction
	in          *instance
}

// hookHandler is an in-process handler for a hook event.
type hookHandler struct {
	event   string
	matcher *regexp.Regexp
	fn      *lua.LFunction
	in      *instance
}

// Argument describes one argument a command takes. It mirrors the
// command-palette argument shape without importing it.
type Argument struct {
	ID          string `json:"id"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// installAPI builds the `harness` global for this instance. Everything
// an extension can reach lives on it.
func (in *instance) installAPI() {
	L := in.L
	api := L.NewTable()

	L.SetFuncs(api, map[string]lua.LGFunction{
		"register_tool":    in.luaRegisterTool,
		"register_command": in.luaRegisterCommand,
		"register_job":     in.luaRegisterJob,
		"on":               in.luaOn,
		"env":              in.luaEnv,
		"workspace":        in.luaWorkspace,
		"exec":             in.luaExec,
		"await":            in.luaAwait,
	})

	api.RawSetString("name", lua.LString(in.ext.Name))
	api.RawSetString("dir", lua.LString(in.ext.Dir))
	api.RawSetString("version", lua.LString(version.Version))

	logTbl := L.NewTable()
	L.SetFuncs(logTbl, map[string]lua.LGFunction{
		"debug": in.logAt(slog.LevelDebug),
		"info":  in.logAt(slog.LevelInfo),
		"warn":  in.logAt(slog.LevelWarn),
		"error": in.logAt(slog.LevelError),
	})
	api.RawSetString("log", logTbl)

	jsonTbl := L.NewTable()
	L.SetFuncs(jsonTbl, map[string]lua.LGFunction{
		"encode": in.luaJSONEncode,
		"decode": in.luaJSONDecode,
	})
	api.RawSetString("json", jsonTbl)

	fsTbl := L.NewTable()
	L.SetFuncs(fsTbl, map[string]lua.LGFunction{
		"read":   in.luaFSRead,
		"write":  in.luaFSWrite,
		"append": in.luaFSAppend,
		"list":   in.luaFSList,
		"exists": in.luaFSExists,
		"mkdir":  in.luaFSMkdir,
	})
	api.RawSetString("fs", fsTbl)

	httpTbl := L.NewTable()
	L.SetFuncs(httpTbl, map[string]lua.LGFunction{
		"request": in.luaHTTPRequest,
		"get":     in.luaHTTPGet,
		"post":    in.luaHTTPPost,
	})
	api.RawSetString("http", httpTbl)

	jobsTbl := L.NewTable()
	L.SetFuncs(jobsTbl, map[string]lua.LGFunction{
		"start":   in.luaJobsStart,
		"status":  in.luaJobsStatus,
		"results": in.luaJobsResults,
		"cancel":  in.luaJobsCancel,
	})
	api.RawSetString("jobs", jobsTbl)

	L.SetGlobal("harness", api)
}

// logAt returns a Lua function that logs at the given level, tagged with
// the extension's name so a noisy extension is identifiable in the log.
func (in *instance) logAt(level slog.Level) lua.LGFunction {
	return func(L *lua.LState) int {
		msg := L.CheckString(1)
		attrs := []any{"extension", in.ext.Name}
		if tbl, ok := L.Get(2).(*lua.LTable); ok {
			tbl.ForEach(func(k, v lua.LValue) {
				if key, ok := luaKeyString(k); ok {
					attrs = append(attrs, key, fromLua(v))
				}
			})
		}
		slog.Log(context.Background(), level, msg, attrs...)
		return 0
	}
}

func (in *instance) luaJSONEncode(L *lua.LState) int {
	data, err := json.Marshal(fromLua(L.CheckAny(1)))
	if err != nil {
		L.RaiseError("json.encode: %v", err)
		return 0
	}
	L.Push(lua.LString(data))
	return 1
}

func (in *instance) luaJSONDecode(L *lua.LState) int {
	value, err := jsonToLua(L, []byte(L.CheckString(1)))
	if err != nil {
		L.RaiseError("json.decode: %v", err)
		return 0
	}
	L.Push(value)
	return 1
}

func (in *instance) luaEnv(L *lua.LState) int {
	value, ok := os.LookupEnv(L.CheckString(1))
	if !ok {
		L.Push(lua.LNil)
		return 1
	}
	L.Push(lua.LString(value))
	return 1
}

func (in *instance) luaWorkspace(L *lua.LState) int {
	tbl := L.NewTable()
	tbl.RawSetString("root", lua.LString(in.host.opts.WorkingDir))
	tbl.RawSetString("data_dir", lua.LString(in.host.opts.DataDir))
	tbl.RawSetString("extension_dir", lua.LString(in.ext.Dir))
	L.Push(tbl)
	return 1
}

// luaRegisterTool implements harness.register_tool.
func (in *instance) luaRegisterTool(L *lua.LState) int {
	spec := L.CheckTable(1)
	if !in.loading {
		L.RaiseError("register_tool must be called while the extension loads")
		return 0
	}

	name := tableString(spec, "name", "")
	if name == "" {
		L.RaiseError("register_tool: name is required")
		return 0
	}
	fn := tableFunc(spec, "handler")
	if fn == nil {
		L.RaiseError("register_tool %q: handler must be a function", name)
		return 0
	}

	properties, required := buildSchema(spec.RawGetString("parameters"))
	in.tools = append(in.tools, &toolSpec{
		name:        name,
		description: tableString(spec, "description", ""),
		parameters:  properties,
		required:    required,
		parallel:    tableBool(spec, "parallel", false),
		fn:          fn,
		in:          in,
	})
	return 0
}

// luaRegisterCommand implements harness.register_command.
func (in *instance) luaRegisterCommand(L *lua.LState) int {
	spec := L.CheckTable(1)
	if !in.loading {
		L.RaiseError("register_command must be called while the extension loads")
		return 0
	}

	name := tableString(spec, "name", "")
	if name == "" {
		L.RaiseError("register_command: name is required")
		return 0
	}
	fn := tableFunc(spec, "handler")
	prompt := tableString(spec, "prompt", "")
	if fn == nil && prompt == "" {
		L.RaiseError("register_command %q: either prompt or handler is required", name)
		return 0
	}

	in.commands = append(in.commands, &commandSpec{
		name:        name,
		description: tableString(spec, "description", ""),
		prompt:      prompt,
		arguments:   buildArguments(spec.RawGetString("arguments")),
		fn:          fn,
		in:          in,
	})
	return 0
}

// luaOn implements harness.on. It accepts either a spec table
// ({event=, matcher=, handler=}) or the shorthand
// harness.on(event, [matcher,] handler).
func (in *instance) luaOn(L *lua.LState) int {
	if !in.loading {
		L.RaiseError("harness.on must be called while the extension loads")
		return 0
	}

	var (
		event   string
		matcher string
		fn      *lua.LFunction
	)
	if tbl, ok := L.Get(1).(*lua.LTable); ok {
		event = tableString(tbl, "event", "")
		matcher = tableString(tbl, "matcher", "")
		fn = tableFunc(tbl, "handler")
	} else {
		event = L.CheckString(1)
		switch arg := L.Get(2).(type) {
		case *lua.LFunction:
			fn = arg
		case lua.LString:
			matcher = string(arg)
			fn, _ = L.Get(3).(*lua.LFunction)
		}
	}

	if !slices.Contains(hooks.EventNames(), event) {
		L.RaiseError("harness.on: unknown event %q", event)
		return 0
	}
	if fn == nil {
		L.RaiseError("harness.on %q: handler must be a function", event)
		return 0
	}

	handler := &hookHandler{event: event, fn: fn, in: in}
	if matcher != "" {
		re, err := regexp.Compile(matcher)
		if err != nil {
			L.RaiseError("harness.on %q: invalid matcher %q: %v", event, matcher, err)
			return 0
		}
		handler.matcher = re
	}
	in.hooks[event] = append(in.hooks[event], handler)
	return 0
}

// buildSchema turns the `parameters` field of a tool spec into JSON
// Schema properties plus a required list. Two shapes are accepted: a
// full schema object (one with a `properties` key), and the shorthand
// that maps each parameter name to its own description, either as a
// string or as a table carrying `type`, `description` and `required`.
func buildSchema(value lua.LValue) (map[string]any, []string) {
	tbl, ok := value.(*lua.LTable)
	if !ok {
		return map[string]any{}, nil
	}

	raw, ok := fromLua(tbl).(map[string]any)
	if !ok {
		return map[string]any{}, nil
	}

	if props, ok := raw["properties"].(map[string]any); ok {
		var required []string
		if list, ok := raw["required"].([]any); ok {
			for _, item := range list {
				if s, ok := item.(string); ok {
					required = append(required, s)
				}
			}
		}
		return props, required
	}

	properties := make(map[string]any, len(raw))
	var required []string
	for name, spec := range raw {
		switch typed := spec.(type) {
		case string:
			properties[name] = map[string]any{"type": "string", "description": typed}
		case map[string]any:
			prop := make(map[string]any, len(typed))
			for k, v := range typed {
				if k == "required" {
					if req, ok := v.(bool); ok && req {
						required = append(required, name)
					}
					continue
				}
				prop[k] = v
			}
			if _, ok := prop["type"]; !ok {
				prop["type"] = "string"
			}
			properties[name] = prop
		default:
			properties[name] = map[string]any{"type": "string"}
		}
	}
	slices.Sort(required)
	return properties, required
}

// buildArguments reads the `arguments` field of a command spec.
func buildArguments(value lua.LValue) []Argument {
	tbl, ok := value.(*lua.LTable)
	if !ok {
		return nil
	}
	var args []Argument
	tbl.ForEach(func(_, v lua.LValue) {
		item, ok := v.(*lua.LTable)
		if !ok {
			return
		}
		id := tableString(item, "id", tableString(item, "name", ""))
		if id == "" {
			return
		}
		args = append(args, Argument{
			ID:          id,
			Title:       tableString(item, "title", id),
			Description: tableString(item, "description", ""),
			Required:    tableBool(item, "required", false),
		})
	})
	return args
}

// luaRegisterJob implements harness.register_job. A job is work too slow
// to hold a tool call open: it runs in a VM of its own, outlives the
// call that started it, and its result waits in the queue until
// something collects it.
func (in *instance) luaRegisterJob(L *lua.LState) int {
	spec := L.CheckTable(1)
	if !in.loading {
		L.RaiseError("register_job must be called while the extension loads")
		return 0
	}

	name := tableString(spec, "name", "")
	if name == "" {
		L.RaiseError("register_job: name is required")
		return 0
	}
	fn := tableFunc(spec, "handler")
	if fn == nil {
		L.RaiseError("register_job %q: handler must be a function", name)
		return 0
	}

	timeout := DefaultJobTimeout
	if seconds, ok := spec.RawGetString("timeout").(lua.LNumber); ok && seconds > 0 {
		timeout = min(time.Duration(float64(seconds)*float64(time.Second)), MaxJobTimeout)
	}

	in.jobSpecs[name] = &jobSpec{
		name:        name,
		description: tableString(spec, "description", ""),
		timeout:     timeout,
		fn:          fn,
	}
	return 0
}

// luaJobsStart implements harness.jobs.start. It returns the job's ID
// straight away; the work carries on in the background.
func (in *instance) luaJobsStart(L *lua.LState) int {
	name := L.CheckString(1)
	spec, ok := in.jobSpecs[name]
	if !ok {
		L.RaiseError("jobs.start: extension %q registers no job %q", in.ext.Name, name)
		return 0
	}

	var args any
	if tbl, ok := L.Get(2).(*lua.LTable); ok {
		args = fromLua(tbl)
	}

	job := in.host.jobs.start(in.ext, spec.name, args, spec.timeout)
	L.Push(lua.LString(job.ID))
	return 1
}

// luaJobsStatus implements harness.jobs.status.
func (in *instance) luaJobsStatus(L *lua.LState) int {
	job, ok := in.host.jobs.get(L.CheckString(1))
	if !ok {
		L.Push(lua.LNil)
		return 1
	}
	L.Push(jobToLua(L, job))
	return 1
}

// luaJobsResults implements harness.jobs.results: it drains the finished
// jobs whose results nothing has collected yet.
func (in *instance) luaJobsResults(L *lua.LState) int {
	finished := in.host.jobs.drain()
	tbl := L.CreateTable(len(finished), 0)
	for _, job := range finished {
		tbl.Append(jobToLua(L, job))
	}
	L.Push(tbl)
	return 1
}

// luaJobsCancel implements harness.jobs.cancel.
func (in *instance) luaJobsCancel(L *lua.LState) int {
	L.Push(lua.LBool(in.host.jobs.cancel(L.CheckString(1))))
	return 1
}

// jobToLua renders a job record for Lua.
func jobToLua(L *lua.LState, job Job) *lua.LTable {
	tbl := L.CreateTable(0, 7)
	tbl.RawSetString("id", lua.LString(job.ID))
	tbl.RawSetString("extension", lua.LString(job.Extension))
	tbl.RawSetString("name", lua.LString(job.Name))
	tbl.RawSetString("state", lua.LString(string(job.State)))
	tbl.RawSetString("result", lua.LString(job.Result))
	tbl.RawSetString("error", lua.LString(job.Err))
	tbl.RawSetString("seconds", lua.LNumber(job.Age().Seconds()))
	return tbl
}
