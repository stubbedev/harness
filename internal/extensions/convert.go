package extensions

import (
	"encoding/json"
	"fmt"
	"math"

	lua "github.com/yuin/gopher-lua"
)

// maxConvertDepth bounds conversion in both directions. Lua tables can
// be cyclic and JSON cannot, so a cycle has to stop somewhere rather
// than recurse until the stack gives out.
const maxConvertDepth = 64

// toLua converts a decoded-JSON Go value into its Lua equivalent.
// Anything it does not recognise becomes nil rather than an error: the
// values reaching it come from JSON, which has no other shapes.
func toLua(L *lua.LState, v any) lua.LValue {
	return toLuaDepth(L, v, 0)
}

func toLuaDepth(L *lua.LState, v any, depth int) lua.LValue {
	if depth > maxConvertDepth {
		return lua.LNil
	}
	switch val := v.(type) {
	case nil:
		return lua.LNil
	case bool:
		return lua.LBool(val)
	case string:
		return lua.LString(val)
	case float64:
		return lua.LNumber(val)
	case float32:
		return lua.LNumber(val)
	case int:
		return lua.LNumber(val)
	case int64:
		return lua.LNumber(val)
	case json.Number:
		if f, err := val.Float64(); err == nil {
			return lua.LNumber(f)
		}
		return lua.LString(val.String())
	case []byte:
		return lua.LString(string(val))
	case []any:
		tbl := L.CreateTable(len(val), 0)
		for _, item := range val {
			tbl.Append(toLuaDepth(L, item, depth+1))
		}
		return tbl
	case map[string]any:
		tbl := L.CreateTable(0, len(val))
		for k, item := range val {
			tbl.RawSetString(k, toLuaDepth(L, item, depth+1))
		}
		return tbl
	case map[string]string:
		tbl := L.CreateTable(0, len(val))
		for k, item := range val {
			tbl.RawSetString(k, lua.LString(item))
		}
		return tbl
	default:
		// Fall back through JSON so structs reach Lua as tables.
		data, err := json.Marshal(val)
		if err != nil {
			return lua.LNil
		}
		var generic any
		if err := json.Unmarshal(data, &generic); err != nil {
			return lua.LNil
		}
		return toLuaDepth(L, generic, depth+1)
	}
}

// fromLua converts a Lua value into a JSON-compatible Go value. Tables
// whose keys are exactly 1..n become slices; every other table becomes a
// map with stringified keys. Functions, userdata and threads have no
// JSON form and convert to nil.
func fromLua(v lua.LValue) any {
	return fromLuaDepth(v, 0)
}

func fromLuaDepth(v lua.LValue, depth int) any {
	if depth > maxConvertDepth {
		return nil
	}
	switch val := v.(type) {
	case *lua.LNilType:
		return nil
	case lua.LBool:
		return bool(val)
	case lua.LString:
		return string(val)
	case lua.LNumber:
		f := float64(val)
		// Lua has one number type; report whole numbers as integers so
		// they survive JSON without a trailing ".0" in tool arguments.
		if f == math.Trunc(f) && math.Abs(f) < math.MaxInt64 {
			return int64(f)
		}
		return f
	case *lua.LTable:
		return tableToGo(val, depth)
	default:
		return nil
	}
}

// tableToGo converts a table to a slice or a map, depending on whether
// its keys form a 1..n sequence.
func tableToGo(tbl *lua.LTable, depth int) any {
	if n := tbl.Len(); n > 0 && isSequence(tbl, n) {
		out := make([]any, 0, n)
		for i := 1; i <= n; i++ {
			out = append(out, fromLuaDepth(tbl.RawGetInt(i), depth+1))
		}
		return out
	}
	out := make(map[string]any)
	tbl.ForEach(func(k, v lua.LValue) {
		key, ok := luaKeyString(k)
		if !ok {
			return
		}
		out[key] = fromLuaDepth(v, depth+1)
	})
	return out
}

// isSequence reports whether the table holds exactly the integer keys
// 1..n and nothing else.
func isSequence(tbl *lua.LTable, n int) bool {
	count := 0
	mixed := false
	tbl.ForEach(func(k, _ lua.LValue) {
		num, ok := k.(lua.LNumber)
		if !ok || float64(num) != math.Trunc(float64(num)) || num < 1 || int(num) > n {
			mixed = true
			return
		}
		count++
	})
	return !mixed && count == n
}

// luaKeyString renders a table key as a map key. Only strings and
// numbers are usable; anything else is dropped.
func luaKeyString(k lua.LValue) (string, bool) {
	switch key := k.(type) {
	case lua.LString:
		return string(key), true
	case lua.LNumber:
		f := float64(key)
		if f == math.Trunc(f) {
			return fmt.Sprintf("%d", int64(f)), true
		}
		return fmt.Sprintf("%g", f), true
	default:
		return "", false
	}
}

// jsonToLua decodes JSON text straight into a Lua value.
func jsonToLua(L *lua.LState, data []byte) (lua.LValue, error) {
	if len(data) == 0 {
		return lua.LNil, nil
	}
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		return lua.LNil, err
	}
	return toLua(L, generic), nil
}

// tableString reads a string field from a table, returning fallback when
// the field is absent or not a string.
func tableString(tbl *lua.LTable, key, fallback string) string {
	if tbl == nil {
		return fallback
	}
	if s, ok := tbl.RawGetString(key).(lua.LString); ok {
		return string(s)
	}
	return fallback
}

// tableBool reads a boolean field from a table.
func tableBool(tbl *lua.LTable, key string, fallback bool) bool {
	if tbl == nil {
		return fallback
	}
	if b, ok := tbl.RawGetString(key).(lua.LBool); ok {
		return bool(b)
	}
	return fallback
}

// tableFunc reads a function field from a table.
func tableFunc(tbl *lua.LTable, key string) *lua.LFunction {
	if tbl == nil {
		return nil
	}
	if fn, ok := tbl.RawGetString(key).(*lua.LFunction); ok {
		return fn
	}
	return nil
}
