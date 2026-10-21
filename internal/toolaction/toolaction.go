package toolaction

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"charm.land/fantasy"
)

type Table[A ~string, H any] struct {
	field    string
	handlers map[A]H
	names    []string
}

func New[A ~string, H any](field string, handlers map[A]H) Table[A, H] {
	names := make([]string, 0, len(handlers))
	for _, name := range slices.Sorted(maps.Keys(handlers)) {
		names = append(names, string(name))
	}
	return Table[A, H]{field: field, handlers: handlers, names: names}
}

func (t Table[A, H]) Names() []string { return slices.Clone(t.names) }

func (t Table[A, H]) Lookup(value A) (H, error) {
	handler, ok := t.handlers[value]
	if !ok {
		return handler, fmt.Errorf("unknown %s %q; available: %s", t.field, value, strings.Join(t.names, ", "))
	}
	return handler, nil
}

type Enum struct {
	Field  string
	Values []string
}

func (t Table[A, H]) Enum() Enum { return Enum{Field: t.field, Values: t.Names()} }

func Advertise(tool fantasy.AgentTool, enums ...Enum) fantasy.AgentTool {
	info := tool.Info()
	for _, enum := range enums {
		if _, ok := info.Parameters[enum.Field].(map[string]any); !ok {
			panic(fmt.Sprintf("toolaction: tool %s has no parameter %q", info.Name, enum.Field))
		}
	}
	return advertisedTool{AgentTool: tool, enums: enums}
}

type advertisedTool struct {
	fantasy.AgentTool
	enums []Enum
}

func (a advertisedTool) Info() fantasy.ToolInfo {
	info := a.AgentTool.Info()
	info.Parameters = maps.Clone(info.Parameters)
	for _, enum := range a.enums {
		property := maps.Clone(info.Parameters[enum.Field].(map[string]any))
		property["enum"] = slices.Clone(enum.Values)
		info.Parameters[enum.Field] = property
	}
	return info
}
