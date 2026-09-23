package extensions

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	lua "github.com/yuin/gopher-lua"
)

// DefaultCallTimeout bounds a single call into an extension: a tool
// handler, a hook handler or a command handler. A script that loops
// forever is cut off here rather than hanging the turn.
const DefaultCallTimeout = 30 * time.Second

// instance is one loaded extension: its VM, the registrations it made
// while loading, and the lock that serialises calls into it. A
// gopher-lua LState is not safe for concurrent use, and tool calls can
// run in parallel, so every entry into the VM goes through call.
type instance struct {
	ext  *Extension
	host *Host

	// sem serialises access to L. It is a one-slot channel rather than a
	// mutex so a waiting call can still honour its context deadline
	// instead of blocking forever behind a stuck handler.
	sem chan struct{}
	L   *lua.LState

	// ctx is the context of the call currently running in this VM. Host
	// functions read it for cancellation and for the session the call
	// belongs to. Written only while the VM lock is held.
	ctx context.Context

	// pending counts async host calls in flight for this VM.
	pending pendingCounter

	tools    []*toolSpec
	commands []*commandSpec
	hooks    map[string][]*hookHandler
	jobSpecs map[string]*jobSpec

	// loading is true only while init.lua runs. Registration is a
	// load-time act: the tool list and hook set are read once when the
	// host finishes loading, so a registration made later would never be
	// seen.
	loading bool

	// closed is set once close has begun tearing the VM down. A call that
	// wins the VM lock after that refuses to run rather than touching a
	// closed state, whose stack is gone.
	closed    atomic.Bool
	closeOnce sync.Once
}

// newInstance builds a sandboxed VM for the extension and installs the
// harness API into it. The returned instance has not run init.lua yet.
func newInstance(host *Host, ext *Extension) *instance {
	in := &instance{
		ext:      ext,
		host:     host,
		sem:      make(chan struct{}, 1),
		hooks:    make(map[string][]*hookHandler),
		jobSpecs: make(map[string]*jobSpec),
	}
	in.L = newSandboxedState(ext.Dir)
	in.registerHandleType()
	in.installAPI()
	return in
}

// spawnLoadedInstance creates a VM for ext and runs its init.lua. On
// load failure the instance is closed and nil is returned with the
// error, so every boot path applies the same create-run-close
// lifecycle.
func spawnLoadedInstance(ctx context.Context, h *Host, ext *Extension) (*instance, error) {
	in := newInstance(h, ext)
	if err := in.load(ctx); err != nil {
		in.close()
		return nil, err
	}
	return in, nil
}

// newSandboxedState builds an LState with only the libraries an
// extension may use. The VM starts empty (SkipOpenLibs) and each library
// is opened deliberately: there is no io library, no debug library, and
// the os library keeps only its clock and environment functions. Every
// capability beyond pure computation is a host function on the `harness`
// table, which is what makes the API surface auditable.
func newSandboxedState(dir string) *lua.LState {
	L := lua.NewState(lua.Options{SkipOpenLibs: true})

	for _, lib := range []struct {
		name string
		open lua.LGFunction
	}{
		{lua.LoadLibName, lua.OpenPackage},
		{lua.BaseLibName, lua.OpenBase},
		{lua.TabLibName, lua.OpenTable},
		{lua.StringLibName, lua.OpenString},
		{lua.MathLibName, lua.OpenMath},
		{lua.CoroutineLibName, lua.OpenCoroutine},
		{lua.OsLibName, lua.OpenOs},
	} {
		L.Push(L.NewFunction(lib.open))
		L.Push(lua.LString(lib.name))
		L.Call(1, 0)
	}

	// Loading code off disk by path is the one hole the base library
	// leaves; require stays, scoped to the extension's own directory.
	for _, name := range []string{"dofile", "loadfile"} {
		L.SetGlobal(name, lua.LNil)
	}

	// The os library is opened for time and environment reads only.
	if osTbl, ok := L.GetGlobal("os").(*lua.LTable); ok {
		for _, name := range []string{"execute", "exit", "remove", "rename", "setenv", "tmpname"} {
			osTbl.RawSetString(name, lua.LNil)
		}
	}

	// require resolves inside the extension directory and nowhere else,
	// so an extension can split itself across files without reaching for
	// modules elsewhere on the machine.
	if pkg, ok := L.GetGlobal("package").(*lua.LTable); ok {
		pkg.RawSetString("path", lua.LString(
			filepath.Join(dir, "?.lua")+";"+filepath.Join(dir, "?", "init.lua"),
		))
		pkg.RawSetString("cpath", lua.LString(""))
	}

	return L
}

// load runs the extension's init.lua, during which it may register
// tools, commands and hook handlers.
func (in *instance) load(ctx context.Context) error {
	if err := in.acquire(ctx); err != nil {
		return err
	}
	defer in.release()

	in.loading = true
	defer func() { in.loading = false }()

	in.L.SetContext(ctx)
	in.ctx = ctx
	defer func() {
		in.L.RemoveContext()
		in.ctx = nil
	}()

	if err := in.L.DoFile(in.ext.EntryFile); err != nil {
		return fmt.Errorf("load extension %q: %w", in.ext.Name, err)
	}
	return nil
}

// call invokes a Lua function with the given arguments and returns its
// single return value, bounded by the host's per-call timeout.
func (in *instance) call(ctx context.Context, fn *lua.LFunction, args ...lua.LValue) (lua.LValue, error) {
	return in.callWithin(ctx, in.host.callTimeout(), fn, args...)
}

// callWithin is call with an explicit bound. Background jobs run under
// a longer one than a tool call gets. The VM is entered by one
// goroutine at a time; a caller whose context expires while waiting
// gives up rather than queueing behind a stuck handler.
func (in *instance) callWithin(ctx context.Context, timeout time.Duration, fn *lua.LFunction, args ...lua.LValue) (lua.LValue, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := in.acquire(ctx); err != nil {
		return lua.LNil, err
	}
	defer in.release()

	in.L.SetContext(ctx)
	in.ctx = ctx
	defer func() {
		in.L.RemoveContext()
		in.ctx = nil
	}()

	top := in.L.GetTop()
	defer in.L.SetTop(top)

	in.L.Push(fn)
	for _, arg := range args {
		in.L.Push(arg)
	}
	if err := in.L.PCall(len(args), 1, nil); err != nil {
		return lua.LNil, fmt.Errorf("extension %q: %w", in.ext.Name, err)
	}
	return in.L.Get(-1), nil
}

func (in *instance) acquire(ctx context.Context) error {
	select {
	case in.sem <- struct{}{}:
	case <-ctx.Done():
		return fmt.Errorf("extension %q is busy: %w", in.ext.Name, ctx.Err())
	}
	if in.closed.Load() {
		in.release()
		return fmt.Errorf("extension %q is closed", in.ext.Name)
	}
	return nil
}

func (in *instance) release() { <-in.sem }

// closeWait bounds how long close waits for a running call to leave the
// VM. Calls are bounded by their own timeout and honour cancellation, so
// the wait only runs out on a handler that ignores both.
const closeWait = 5 * time.Second

// close tears the VM down. Safe to call more than once. It takes the VM
// lock first so a running call is never pulled out from under itself; if
// that call will not finish, the VM is left to the garbage collector
// rather than closed while in use.
func (in *instance) close() {
	in.closeOnce.Do(func() {
		in.closed.Store(true)
		select {
		case in.sem <- struct{}{}:
			in.L.Close()
			in.release()
		case <-time.After(closeWait):
			slog.Warn("Extension still busy at shutdown, leaving its VM open", "extension", in.ext.Name)
		}
	})
}
