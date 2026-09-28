package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// inMemorySubtrees are the config paths whose values only the UI reads:
// nothing derived from them (providers, models, tools) needs rebuilding
// when they change, so a write under them is applied to the live config in
// place of a full reload.
var inMemorySubtrees = []string{
	"options.tui.",
	"options.notifications",
	"options.disabled_subagents",
	"recent_models.",
}

// inMemoryApplicable reports whether every key of kv lies under one of
// inMemorySubtrees.
func inMemoryApplicable(kv map[string]any) bool {
	if len(kv) == 0 {
		return false
	}
	for key := range kv {
		ok := false
		for _, prefix := range inMemorySubtrees {
			if key == strings.TrimSuffix(prefix, ".") || strings.HasPrefix(key, prefix) {
				ok = true
				break
			}
		}
		// Keybind action names hold dots of their own, which a dotted
		// path cannot address; escapes and wildcards are sjson syntax
		// this walker does not speak.
		if !ok || strings.HasPrefix(key, "options.tui.keybinds") || strings.ContainsAny(key, `\*?#@|`) {
			return false
		}
	}
	return true
}

// applyConfigPaths sets every key of kv in c with setConfigPath.
func applyConfigPaths(c *Config, kv map[string]any) error {
	for key, value := range kv {
		if err := setConfigPath(c, key, value); err != nil {
			return err
		}
	}
	return nil
}

// setConfigPath sets the field at the dotted JSON path in c to value,
// converted through JSON the way the loader would read it from a file.
// Structs along the path are followed by JSON tag; a nil pointer to a
// struct is allocated and a map entry is replaced whole. c must be a
// config clone the caller owns.
func setConfigPath(c *Config, path string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	v := reflect.ValueOf(c).Elem()
	segments := strings.Split(path, ".")
	for i, segment := range segments {
		for v.Kind() == reflect.Pointer {
			if v.IsNil() {
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		last := i == len(segments)-1
		switch v.Kind() {
		case reflect.Struct:
			field, ok := fieldByJSONName(v, segment)
			if !ok {
				return fmt.Errorf("config has no field %q in %s", segment, path)
			}
			v = field
		case reflect.Map:
			if !last {
				return fmt.Errorf("config path %s descends into a map entry", path)
			}
			if v.IsNil() {
				v.Set(reflect.MakeMap(v.Type()))
			}
			elem := reflect.New(v.Type().Elem())
			if err := json.Unmarshal(encoded, elem.Interface()); err != nil {
				return err
			}
			v.SetMapIndex(reflect.ValueOf(segment).Convert(v.Type().Key()), elem.Elem())
			return nil
		default:
			return fmt.Errorf("config path %s descends into a %s", path, v.Kind())
		}
	}
	target := reflect.New(v.Type())
	if err := json.Unmarshal(encoded, target.Interface()); err != nil {
		return err
	}
	v.Set(target.Elem())
	return nil
}

// fieldByJSONName returns the field of struct v whose JSON name is name.
func fieldByJSONName(v reflect.Value, name string) (reflect.Value, bool) {
	t := v.Type()
	for i := range t.NumField() {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if tag == name && t.Field(i).IsExported() {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}
