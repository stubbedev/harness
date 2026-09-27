package dialog

import (
	"strings"

	"github.com/stubbedev/harness/internal/commands"
)

// Commands are invoked from the editor by name ("/compact keep the API
// notes") as well as picked from the palette. Both reach the same
// command item: the names it answers to are derived from what it is (its
// ID, or the label a custom command or skill was loaded under, see
// commands.InvocationNames), and what it takes after its name from its
// action's ArgSpec, so neither can be set out of step with the command.

// builtinSlashNames derives a built-in command's names from its ID and
// aliases: "new_session" answers to "new-session" and "new_session".
func builtinSlashNames(id string, aliases []string) []string {
	names := []string{strings.ReplaceAll(id, "_", "-")}
	if strings.Contains(id, "_") {
		names = append(names, id)
	}
	return append(names, aliases...)
}

// FindInvocable returns the command answering to name, matched without
// regard to case.
func FindInvocable(items []*CommandItem, name string) *CommandItem {
	for _, item := range items {
		for _, n := range item.SlashNames() {
			if strings.EqualFold(n, name) {
				return item
			}
		}
	}
	return nil
}

// argSpecOf returns the ArgSpec of an action that takes arguments.
func argSpecOf(action Action) (commands.ArgSpec, bool) {
	if a, ok := action.(ArgAction); ok {
		return a.ArgSpec(), true
	}
	return commands.ArgSpec{}, false
}

// Invoke resolves an invocation: an ActionRun with raw bound to the
// command's arguments, or an ActionOpenArguments when a required field
// is missing.
// It errors when raw is given to a command that takes nothing.
func Invoke(inv ActionInvoke) (Action, error) {
	a, ok := inv.Action.(ArgAction)
	if !ok {
		if strings.TrimSpace(inv.Raw) != "" {
			return nil, noArgsError(inv.Name)
		}
		return ActionRun{Action: inv.Action}, nil
	}
	spec := a.ArgSpec()
	args, err := spec.Bind(inv.Raw)
	if err != nil {
		return nil, noArgsError(inv.Name)
	}
	if !spec.Complete(args) {
		return ActionOpenArguments{Action: a, Args: args}, nil
	}
	return ActionRun{Action: a.WithArgs(args)}, nil
}

type noArgsError string

func (e noArgsError) Error() string { return "/" + string(e) + " takes no arguments" }
