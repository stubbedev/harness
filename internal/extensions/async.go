package extensions

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/stubbedev/harness/internal/crash"
	lua "github.com/yuin/gopher-lua"
)

// maxPendingCalls bounds how many async calls one extension may have in
// flight. A script that starts work faster than it awaits it would
// otherwise queue goroutines without limit; hitting the cap is an error
// the script sees, which is the backpressure.
const maxPendingCalls = 64

// handleTypeName is the metatable name of an async handle.
const handleTypeName = "harness.handle"

// pendingCall is one async host call: the goroutine writes the result,
// closes done, and never touches Lua. The awaiting call converts it on
// the VM's own goroutine, which is what keeps the single-threaded VM
// single-threaded while the I/O runs in parallel.
type pendingCall struct {
	kind string
	done chan struct{}

	exec execResult
	http httpResult
	err  error
}

// wait blocks until the call finishes, the context ends, or the whole
// host shuts down.
func (p *pendingCall) wait(ctx context.Context) error {
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// push renders the finished call. Exactly one value is pushed whatever
// the outcome, so awaiting several handles returns one result each, in
// the order they were passed.
func (p *pendingCall) push(L *lua.LState) int {
	switch p.kind {
	case "exec":
		return pushExecResult(L, p.exec)
	case "http":
		if p.err != nil {
			// The sync path reports a transport error as a second return
			// value. An await of several handles has no room for that, so
			// the error rides in the result table instead.
			tbl := L.CreateTable(0, 2)
			tbl.RawSetString("ok", lua.LFalse)
			tbl.RawSetString("error", lua.LString(p.err.Error()))
			L.Push(tbl)
			return 1
		}
		return pushHTTPResult(L, p.http, nil)
	default:
		L.Push(lua.LNil)
		return 1
	}
}

// pushPending starts work on its own goroutine and pushes the handle
// that awaits it. The context is the running call's, so work started by
// a handler is cancelled when that handler returns: a handle does not
// outlive the call that made it. Work that has to outlive a call is a
// job (see jobs.go).
func (in *instance) pushPending(L *lua.LState, kind string, run func(*pendingCall, context.Context)) int {
	if in.pending.Load() >= maxPendingCalls {
		L.RaiseError("too many pending async calls (limit %d); await some before starting more", maxPendingCalls)
		return 0
	}

	ctx := in.callContext()
	p := &pendingCall{kind: kind, done: make(chan struct{})}
	in.pending.Add(1)
	go func() {
		defer func() {
			in.pending.Add(-1)
			close(p.done)
		}()
		defer crash.Recover("extensions.async", nil)
		run(p, ctx)
	}()

	ud := L.NewUserData()
	ud.Value = p
	L.SetMetatable(ud, L.GetTypeMetatable(handleTypeName))
	L.Push(ud)
	return 1
}

// registerHandleType installs the handle metatable, which carries the
// method form of await so `handle:await()` reads as well as
// `harness.await(handle)`.
func (in *instance) registerHandleType() {
	mt := in.L.NewTypeMetatable(handleTypeName)
	methods := in.L.NewTable()
	in.L.SetFuncs(methods, map[string]lua.LGFunction{
		"await": in.luaAwait,
	})
	mt.RawSetString("__index", methods)
	mt.RawSetString("__tostring", in.L.NewFunction(func(L *lua.LState) int {
		p, err := checkPending(L, 1)
		if err != nil {
			L.Push(lua.LString("harness.handle(invalid)"))
			return 1
		}
		L.Push(lua.LString(fmt.Sprintf("harness.handle(%s)", p.kind)))
		return 1
	}))
}

// luaAwait implements harness.await. It takes one or more handles and
// returns one result per handle, in order.
func (in *instance) luaAwait(L *lua.LState) int {
	count := L.GetTop()
	if count == 0 {
		L.RaiseError("await: at least one handle is required")
		return 0
	}

	pendings := make([]*pendingCall, 0, count)
	for i := 1; i <= count; i++ {
		p, err := checkPending(L, i)
		if err != nil {
			L.RaiseError("await: argument %d: %v", i, err)
			return 0
		}
		pendings = append(pendings, p)
	}

	ctx := in.callContext()
	pushed := 0
	for _, p := range pendings {
		if err := p.wait(ctx); err != nil {
			L.RaiseError("await: %v", err)
			return 0
		}
		pushed += p.push(L)
	}
	return pushed
}

// checkPending reads a handle off the stack.
func checkPending(L *lua.LState, index int) (*pendingCall, error) {
	ud, ok := L.Get(index).(*lua.LUserData)
	if !ok {
		return nil, fmt.Errorf("expected a handle, got %s", L.Get(index).Type())
	}
	p, ok := ud.Value.(*pendingCall)
	if !ok {
		return nil, fmt.Errorf("expected a handle")
	}
	return p, nil
}

// pendingCounter is the in-flight async counter carried per instance.
type pendingCounter = atomic.Int64
