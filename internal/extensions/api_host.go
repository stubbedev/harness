package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/stubbedev/harness/internal/shell"
	lua "github.com/yuin/gopher-lua"
)

// Synthetic tool names for the privileged host functions. Hooks see them
// exactly as they see a built-in tool call, so a PreToolUse policy can
// match on them and deny what an extension is about to do.
const (
	GateRead  = "extension_read"
	GateWrite = "extension_write"
	GateExec  = "extension_exec"
	GateHTTP  = "extension_http"
)

// maxHTTPBody bounds a response an extension reads into the VM. A
// response larger than this is truncated rather than loaded whole.
const maxHTTPBody = 8 << 20

// resolvePath makes a relative path absolute against the workspace root,
// so an extension can address project files the way the user does.
func (in *instance) resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(in.host.opts.WorkingDir, path)
}

func (in *instance) luaFSRead(L *lua.LState) int {
	path := in.resolvePath(L.CheckString(1))
	if err := in.gate(GateRead, map[string]any{"path": path}); err != nil {
		L.RaiseError("fs.read: %v", err)
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	L.Push(lua.LString(data))
	return 1
}

func (in *instance) luaFSWrite(L *lua.LState) int {
	return in.writeFile(L, false)
}

func (in *instance) luaFSAppend(L *lua.LState) int {
	return in.writeFile(L, true)
}

func (in *instance) writeFile(L *lua.LState, appendMode bool) int {
	path := in.resolvePath(L.CheckString(1))
	content := L.CheckString(2)
	if err := in.gate(GateWrite, map[string]any{
		"path":    path,
		"content": content,
		"append":  appendMode,
	}); err != nil {
		L.RaiseError("fs.write: %v", err)
		return 0
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		L.Push(lua.LFalse)
		L.Push(lua.LString(err.Error()))
		return 2
	}

	var err error
	if appendMode {
		var f *os.File
		f, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			_, err = f.WriteString(content)
			err = errJoin(err, f.Close())
		}
	} else {
		err = os.WriteFile(path, []byte(content), 0o644)
	}
	if err != nil {
		L.Push(lua.LFalse)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	L.Push(lua.LTrue)
	return 1
}

func (in *instance) luaFSList(L *lua.LState) int {
	path := in.resolvePath(L.CheckString(1))
	if err := in.gate(GateRead, map[string]any{"path": path}); err != nil {
		L.RaiseError("fs.list: %v", err)
		return 0
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	tbl := L.CreateTable(len(entries), 0)
	for _, entry := range entries {
		item := L.CreateTable(0, 2)
		item.RawSetString("name", lua.LString(entry.Name()))
		item.RawSetString("dir", lua.LBool(entry.IsDir()))
		tbl.Append(item)
	}
	L.Push(tbl)
	return 1
}

func (in *instance) luaFSExists(L *lua.LState) int {
	_, err := os.Stat(in.resolvePath(L.CheckString(1)))
	L.Push(lua.LBool(err == nil))
	return 1
}

func (in *instance) luaFSMkdir(L *lua.LState) int {
	path := in.resolvePath(L.CheckString(1))
	if err := in.gate(GateWrite, map[string]any{"path": path, "mkdir": true}); err != nil {
		L.RaiseError("fs.mkdir: %v", err)
		return 0
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		L.Push(lua.LFalse)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	L.Push(lua.LTrue)
	return 1
}

// execRequest is one command to run through the embedded shell.
type execRequest struct {
	command string
	cwd     string
	stdin   string
}

// execResult is what a command left behind.
type execResult struct {
	stdout string
	stderr string
	code   int
	ok     bool
}

// runExec runs a command through the same embedded POSIX shell the
// shell tool and hooks use, so an extension sees the same interpreter
// and builtins as everything else in Harness. It holds no Lua state, so
// it is also what the async path runs on its own goroutine.
func (in *instance) runExec(ctx context.Context, req execRequest) execResult {
	var stdout, stderr bytes.Buffer
	err := shell.Run(ctx, shell.RunOptions{
		Command: req.command,
		Cwd:     req.cwd,
		Stdin:   strings.NewReader(req.stdin),
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	return execResult{
		stdout: stdout.String(),
		stderr: stderr.String(),
		code:   shell.ExitCode(err),
		ok:     err == nil,
	}
}

// pushExecResult renders a finished command as the table Lua sees.
func pushExecResult(L *lua.LState, result execResult) int {
	tbl := L.CreateTable(0, 4)
	tbl.RawSetString("stdout", lua.LString(result.stdout))
	tbl.RawSetString("stderr", lua.LString(result.stderr))
	tbl.RawSetString("code", lua.LNumber(result.code))
	tbl.RawSetString("ok", lua.LBool(result.ok))
	L.Push(tbl)
	return 1
}

// luaExec implements harness.exec. With opts.async it returns a handle
// to await instead of the result.
func (in *instance) luaExec(L *lua.LState) int {
	opts, _ := L.Get(2).(*lua.LTable)
	req := execRequest{
		command: L.CheckString(1),
		cwd:     in.resolvePath(tableString(opts, "cwd", ".")),
		stdin:   tableString(opts, "stdin", ""),
	}

	if err := in.gate(GateExec, map[string]any{"command": req.command, "cwd": req.cwd}); err != nil {
		L.RaiseError("exec: %v", err)
		return 0
	}

	if tableBool(opts, "async", false) {
		return in.pushPending(L, "exec", func(p *pendingCall, ctx context.Context) {
			p.exec = in.runExec(ctx, req)
		})
	}
	return pushExecResult(L, in.runExec(in.callContext(), req))
}

// httpRequestSpec is one request an extension asked for.
type httpRequestSpec struct {
	url     string
	method  string
	body    string
	headers map[string]string
}

// httpResult is a response, read into memory up to maxHTTPBody.
type httpResult struct {
	status  int
	ok      bool
	body    string
	headers map[string]string
}

// runHTTP performs a request. Like runExec it touches no Lua state, so
// the async path can run it on its own goroutine.
func (in *instance) runHTTP(ctx context.Context, spec httpRequestSpec) (httpResult, error) {
	req, err := http.NewRequestWithContext(ctx, spec.method, spec.url, strings.NewReader(spec.body))
	if err != nil {
		return httpResult{}, err
	}
	for key, value := range spec.headers {
		req.Header.Set(key, value)
	}
	if spec.body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	rsp, err := in.host.httpClient().Do(req)
	if err != nil {
		return httpResult{}, err
	}
	defer rsp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(rsp.Body, maxHTTPBody))
	if err != nil {
		return httpResult{}, err
	}

	headers := make(map[string]string, len(rsp.Header))
	for key := range rsp.Header {
		headers[strings.ToLower(key)] = rsp.Header.Get(key)
	}
	return httpResult{
		status:  rsp.StatusCode,
		ok:      rsp.StatusCode >= 200 && rsp.StatusCode < 300,
		body:    string(data),
		headers: headers,
	}, nil
}

// pushHTTPResult renders a response as the table Lua sees. A transport
// error becomes nil plus the message, the shape Lua callers expect.
func pushHTTPResult(L *lua.LState, result httpResult, err error) int {
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	tbl := L.CreateTable(0, 4)
	tbl.RawSetString("status", lua.LNumber(result.status))
	tbl.RawSetString("ok", lua.LBool(result.ok))
	tbl.RawSetString("body", lua.LString(result.body))
	headers := L.CreateTable(0, len(result.headers))
	for key, value := range result.headers {
		headers.RawSetString(key, lua.LString(value))
	}
	tbl.RawSetString("headers", headers)
	L.Push(tbl)
	return 1
}

// readHTTPSpec reads a request table. Returns false when the table has
// no url, having already raised the error.
func readHTTPSpec(L *lua.LState, tbl *lua.LTable) (httpRequestSpec, bool) {
	spec := httpRequestSpec{
		url:     tableString(tbl, "url", ""),
		method:  strings.ToUpper(tableString(tbl, "method", http.MethodGet)),
		body:    requestBody(tbl),
		headers: map[string]string{},
	}
	if spec.url == "" {
		L.RaiseError("http.request: url is required")
		return spec, false
	}
	if headers, ok := tbl.RawGetString("headers").(*lua.LTable); ok {
		headers.ForEach(func(k, v lua.LValue) {
			if key, ok := luaKeyString(k); ok {
				spec.headers[key] = v.String()
			}
		})
	}
	return spec, true
}

// luaHTTPRequest implements harness.http.request. It takes a table with
// url, method, headers and body, and returns status, body and headers.
// With async = true it returns a handle to await instead.
func (in *instance) luaHTTPRequest(L *lua.LState) int {
	tbl := L.CheckTable(1)
	spec, ok := readHTTPSpec(L, tbl)
	if !ok {
		return 0
	}

	if err := in.gate(GateHTTP, map[string]any{"url": spec.url, "method": spec.method}); err != nil {
		L.RaiseError("http.request: %v", err)
		return 0
	}

	if tableBool(tbl, "async", false) {
		return in.pushPending(L, "http", func(p *pendingCall, ctx context.Context) {
			p.http, p.err = in.runHTTP(ctx, spec)
		})
	}
	result, err := in.runHTTP(in.callContext(), spec)
	return pushHTTPResult(L, result, err)
}

func (in *instance) luaHTTPGet(L *lua.LState) int {
	spec := L.CreateTable(0, 2)
	spec.RawSetString("url", lua.LString(L.CheckString(1)))
	spec.RawSetString("method", lua.LString(http.MethodGet))
	if headers, ok := L.Get(2).(*lua.LTable); ok {
		spec.RawSetString("headers", headers)
	}
	L.SetTop(0)
	L.Push(spec)
	return in.luaHTTPRequest(L)
}

func (in *instance) luaHTTPPost(L *lua.LState) int {
	spec := L.CreateTable(0, 3)
	spec.RawSetString("url", lua.LString(L.CheckString(1)))
	spec.RawSetString("method", lua.LString(http.MethodPost))
	spec.RawSetString("body", L.Get(2))
	if headers, ok := L.Get(3).(*lua.LTable); ok {
		spec.RawSetString("headers", headers)
	}
	L.SetTop(0)
	L.Push(spec)
	return in.luaHTTPRequest(L)
}

// requestBody renders the body field: a string is sent as-is, a table is
// encoded as JSON.
func requestBody(spec *lua.LTable) string {
	switch body := spec.RawGetString("body").(type) {
	case lua.LString:
		return string(body)
	case *lua.LTable:
		data, err := json.Marshal(fromLua(body))
		if err != nil {
			return ""
		}
		return string(data)
	default:
		return ""
	}
}

// callContext returns the context of the call currently running in this
// instance, falling back to the background context when a host function
// is somehow reached outside one.
func (in *instance) callContext() context.Context {
	if in.ctx != nil {
		return in.ctx
	}
	return context.Background()
}

// errJoin returns the first non-nil error of the two.
func errJoin(a, b error) error {
	if a != nil {
		return a
	}
	return b
}
