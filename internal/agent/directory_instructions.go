package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/filepathext"
	"github.com/stubbedev/harness/internal/skills"
)

const (
	directoryInstructionFileLimit  = 32 << 10
	directoryInstructionTotalLimit = 96 << 10
	directoryInstructionCountLimit = 128
	directoryInstructionRetry      = "Directory instructions require review. No mutation was executed. Read the instructions, then explicitly retry this operation in your next model turn."
)

var directoryInstructionDefaults = []string{
	".github/copilot-instructions.md", ".cursorrules",
	"CLAUDE.md", "CLAUDE.local.md", "GEMINI.md", "gemini.md",
	"harness.md", "harness.local.md", "Harness.md", "Harness.local.md",
	"HARNESS.md", "HARNESS.local.md", "AGENTS.md", "agents.md", "Agents.md",
}

type DirectoryInstructions struct {
	mu       sync.Mutex
	root     string
	names    []string
	initErr  error
	excluded map[string][32]byte
	sessions map[string]*directoryInstructionSession
}

type directoryInstructionSession struct {
	active       map[string]directoryInstruction
	acknowledged map[string][32]byte
}

type directoryInstruction struct {
	scope string
	body  string
	hash  [32]byte
}

type directoryInstructionTool struct {
	fantasy.AgentTool
	tracker *DirectoryInstructions
}

func NewDirectoryInstructions(root string, contextPaths []string) *DirectoryInstructions {
	d := &DirectoryInstructions{
		excluded: make(map[string][32]byte),
		sessions: make(map[string]*directoryInstructionSession),
	}
	d.root, d.initErr = filepath.Abs(root)
	if d.initErr == nil {
		d.root, d.initErr = filepath.EvalSymlinks(d.root)
	}
	for _, name := range append(slices.Clone(directoryInstructionDefaults), contextPaths...) {
		if filepath.IsAbs(name) || strings.HasSuffix(name, "/") || strings.ContainsAny(name, "*?[") {
			continue
		}
		// In a subdirectory only the upper-case spelling is an
		// instruction file. A lower-case harness.md deep in a tree is
		// something else - this repository documents its `harness` tool
		// in one - and it was injected as project instructions.
		if base := filepath.Base(name); strings.EqualFold(base, "harness.md") || strings.EqualFold(base, "harness.local.md") {
			if base != "HARNESS.md" && base != "HARNESS.local.md" {
				continue
			}
		}
		name = filepath.Clean(name)
		if name == "." || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			continue
		}
		d.names = append(d.names, name)
	}
	slices.Sort(d.names)
	d.names = slices.Compact(d.names)
	return d
}

func (d *DirectoryInstructions) ExcludePromptPaths(paths []string) {
	if d == nil || d.initErr != nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	root, err := os.OpenRoot(d.root)
	if err != nil {
		return
	}
	defer root.Close()
	for _, path := range paths {
		joined := filepathext.SmartJoin(d.root, path)
		if resolved, ok := directoryInstructionName(root, d.root, d.root, filepath.ToSlash(path)); ok {
			joined = resolved
		}
		if instruction, err := d.read(joined); err == nil && instruction.body != "" {
			d.excluded[joined] = instruction.hash
		}
	}
}

func (d *DirectoryInstructions) WrapTools(input []fantasy.AgentTool) []fantasy.AgentTool {
	if d == nil {
		return input
	}
	output := slices.Clone(input)
	for i, tool := range output {
		if mcp, ok := tool.(interface{ MCP() string }); ok && mcp.MCP() != "" {
			continue
		}
		switch tool.Info().Name {
		case tools.ViewToolName, tools.EditToolName, tools.WriteToolName, tools.LSPToolName, tools.ShellToolName:
			if wrapped, ok := tool.(*directoryInstructionTool); !ok || wrapped.tracker != d {
				output[i] = &directoryInstructionTool{AgentTool: tool, tracker: d}
			}
		}
	}
	return output
}

func (t *directoryInstructionTool) MCP() string {
	if inner, ok := t.AgentTool.(interface{ MCP() string }); ok {
		return inner.MCP()
	}
	return ""
}

func (t *directoryInstructionTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	paths, mutation := directoryInstructionPaths(t.Info().Name, call.Input)
	if len(paths) == 0 {
		return t.AgentTool.Run(ctx, call)
	}
	if err := ctx.Err(); err != nil {
		return fantasy.ToolResponse{}, err
	}
	note, pending, err := t.tracker.activate(ctx, paths)
	if err != nil {
		note = appendNote(note, "Directory instructions: "+err.Error())
	}
	if mutation && (pending || err != nil) {
		return fantasy.NewTextErrorResponse(appendNote(note, directoryInstructionRetry)), nil
	}
	response, runErr := t.AgentTool.Run(ctx, call)
	if note != "" {
		response.Content = appendNote(response.Content, note)
	}
	return response, runErr
}

func directoryInstructionPaths(name, input string) ([]string, bool) {
	var args struct {
		FilePath   string                  `json:"file_path"`
		Path       string                  `json:"path"`
		WorkingDir string                  `json:"working_dir"`
		Action     string                  `json:"action"`
		Files      []tools.ViewFileRequest `json:"files"`
	}
	if json.Unmarshal([]byte(input), &args) != nil {
		return nil, false
	}
	var paths []string
	mutation := false
	switch name {
	case tools.ViewToolName:
		if args.FilePath != "" && len(args.Files) > 0 || len(args.Files) > tools.MaxViewFilesPerCall {
			return nil, false
		}
		paths = append(paths, args.FilePath)
		for _, file := range args.Files {
			paths = append(paths, file.FilePath)
		}
	case tools.EditToolName, tools.WriteToolName:
		paths = append(paths, args.FilePath)
		mutation = true
	case tools.ShellToolName:
		paths = append(paths, args.WorkingDir)
	case tools.LSPToolName:
		switch args.Action {
		case "symbols", "replace_symbol":
			paths = append(paths, args.FilePath)
		case "diagnostics":
			if args.FilePath == "" {
				args.FilePath = "."
			}
			paths = append(paths, args.FilePath)
		case "definition", "references", "call_hierarchy", "rename":
			if args.Path == "" {
				args.Path = "."
			}
			paths = append(paths, args.Path)
		}
		mutation = args.Action == "replace_symbol" || args.Action == "rename"
	}
	return slices.DeleteFunc(paths, func(path string) bool { return path == "" || strings.HasPrefix(path, skills.BuiltinPrefix) }), mutation
}

func (d *DirectoryInstructions) session(ctx context.Context) *directoryInstructionSession {
	id := tools.GetSessionFromContext(ctx)
	s := d.sessions[id]
	if s == nil {
		s = &directoryInstructionSession{active: make(map[string]directoryInstruction), acknowledged: make(map[string][32]byte)}
		d.sessions[id] = s
	}
	return s
}

func (d *DirectoryInstructions) activate(ctx context.Context, paths []string) (string, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.initErr != nil {
		return "", false, fmt.Errorf("cannot resolve workspace root: %w", d.initErr)
	}
	candidates := make(map[string]string)
	instructionRoot, err := os.OpenRoot(d.root)
	if err != nil {
		return "", false, fmt.Errorf("open instruction root: %w", err)
	}
	defer instructionRoot.Close()
	for _, path := range paths {
		dir, err := d.scope(path)
		if err != nil {
			return "", false, err
		}
		for dir != "" {
			for _, name := range d.names {
				if resolved, ok := directoryInstructionName(instructionRoot, d.root, dir, name); ok {
					candidates[resolved] = dir
				}
			}
			if dir == d.root {
				break
			}
			dir = filepath.Dir(dir)
		}
	}
	s := d.session(ctx)
	loaded := make(map[string]directoryInstruction)
	for _, path := range directoryInstructionOrder(candidates) {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		instruction, err := d.read(path, candidates[path])
		if err != nil {
			return "", false, err
		}
		if instruction.body == "" {
			delete(s.active, path)
			delete(s.acknowledged, path)
			continue
		}
		if hash, ok := d.excluded[path]; ok && hash == instruction.hash {
			delete(s.active, path)
			delete(s.acknowledged, path)
			continue
		}
		loaded[path] = instruction
	}
	total, count := 0, len(loaded)
	for path, instruction := range s.active {
		if _, exists := loaded[path]; !exists {
			total += len(instruction.body)
			count++
		}
	}
	for _, instruction := range loaded {
		total += len(instruction.body)
	}
	if count > directoryInstructionCountLimit || total > directoryInstructionTotalLimit {
		return "", false, fmt.Errorf("active instruction budget exceeded (%d files, %d bytes; limits %d files, %d bytes); no instruction bodies were truncated", count, total, directoryInstructionCountLimit, directoryInstructionTotalLimit)
	}
	var bodies []string
	pending := false
	for _, path := range directoryInstructionOrder(loaded) {
		instruction := loaded[path]
		if old, exists := s.active[path]; !exists || old.hash != instruction.hash {
			bodies = append(bodies, instruction.body)
		}
		s.active[path] = instruction
		if hash, ok := s.acknowledged[path]; !ok || hash != instruction.hash {
			pending = true
		}
	}
	return strings.Join(bodies, "\n\n"), pending, nil
}

func (d *DirectoryInstructions) scope(path string) (string, error) {
	path = filepath.Clean(filepathext.SmartJoin(d.root, path))
	resolved, err := directoryInstructionResolve(path)
	if err != nil {
		return "", fmt.Errorf("resolve instruction scope %q: %w", path, err)
	}
	if _, inside := filepathext.RelWithin(d.root, resolved); !inside {
		return "", nil
	}
	info, err := os.Stat(resolved)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect instruction scope %q: %w", path, err)
	}
	if info == nil || !info.IsDir() {
		resolved = filepath.Dir(resolved)
	}
	return resolved, nil
}

func directoryInstructionResolve(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return resolved, err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err = directoryInstructionResolve(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}

// directoryInstructionName resolves a configured instruction name against
// the directory listing of dir, component by component, requiring exact
// name matches. On case-insensitive filesystems a Stat of every configured
// casing variant would find the same file several times and load its body
// once per variant; matching listing entries keeps discovery exact and the
// resulting path carries the on-disk casing.
func directoryInstructionName(root *os.Root, workspace, dir, name string) (string, bool) {
	current := dir
	for part := range strings.SplitSeq(filepath.ToSlash(name), "/") {
		rel, inside := filepathext.RelWithin(workspace, current)
		if !inside {
			return "", false
		}
		entries, ok := directoryInstructionEntries(root, rel)
		if !ok {
			return "", false
		}
		found := ""
		for _, entry := range entries {
			if entry.Name() == part {
				found = entry.Name()
				break
			}
		}
		if found == "" {
			return "", false
		}
		current = filepath.Join(current, found)
	}
	return current, true
}

func directoryInstructionEntries(root *os.Root, rel string) ([]os.DirEntry, bool) {
	file, err := root.Open(rel)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	entries, err := file.ReadDir(-1)
	if err != nil {
		return nil, false
	}
	return entries, true
}

func (d *DirectoryInstructions) read(path string, scopes ...string) (directoryInstruction, error) {
	var instruction directoryInstruction
	rel, inside := filepathext.RelWithin(d.root, path)
	if !inside {
		return instruction, nil
	}
	root, err := os.OpenRoot(d.root)
	if err != nil {
		return instruction, fmt.Errorf("open instruction root: %w", err)
	}
	defer root.Close()
	info, err := root.Stat(rel)
	if errors.Is(err, os.ErrNotExist) {
		return instruction, nil
	}
	if err != nil {
		return instruction, fmt.Errorf("read instruction %q: %w", path, err)
	}
	if info.IsDir() {
		return instruction, nil
	}
	if !info.Mode().IsRegular() {
		return instruction, fmt.Errorf("instruction %q is not a regular file", path)
	}
	file, err := root.Open(rel)
	if errors.Is(err, os.ErrNotExist) {
		return instruction, nil
	}
	if err != nil {
		return instruction, fmt.Errorf("read instruction %q: %w", path, err)
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return instruction, fmt.Errorf("inspect instruction %q: %w", path, err)
	}
	if info.IsDir() {
		return instruction, nil
	}
	if !info.Mode().IsRegular() {
		return instruction, fmt.Errorf("instruction %q is not a regular file", path)
	}
	body, err := io.ReadAll(io.LimitReader(file, directoryInstructionFileLimit+1))
	if err != nil {
		return instruction, fmt.Errorf("read instruction %q: %w", path, err)
	}
	if len(body) > directoryInstructionFileLimit {
		return instruction, fmt.Errorf("instruction %q exceeds the %d-byte per-file limit; no instruction body was truncated", path, directoryInstructionFileLimit)
	}
	instruction.scope = filepath.Dir(path)
	if len(scopes) > 0 {
		instruction.scope = scopes[0]
	}
	instruction.hash = sha256.Sum256(body)
	instruction.body = fmt.Sprintf("<directory_instructions path=%q scope=%q version=%x>\nApplies only within this directory scope; deeper instructions take precedence.\n%s\n</directory_instructions>", path, instruction.scope, instruction.hash, body)
	return instruction, nil
}

func directoryInstructionOrder[V any](entries map[string]V) []string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	scope := func(path string) string {
		switch entry := any(entries[path]).(type) {
		case string:
			return entry
		case directoryInstruction:
			return entry.scope
		default:
			return filepath.Dir(path)
		}
	}
	slices.SortFunc(keys, func(a, b string) int {
		depthA := strings.Count(scope(a), string(filepath.Separator))
		depthB := strings.Count(scope(b), string(filepath.Separator))
		if depthA != depthB {
			return depthA - depthB
		}
		return strings.Compare(a, b)
	})
	return keys
}

func (d *DirectoryInstructions) Prepare(ctx context.Context, messages []fantasy.Message) []fantasy.Message {
	if d == nil || ctx.Err() != nil {
		return messages
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.session(ctx)
	total := 0
	for _, path := range directoryInstructionOrder(s.active) {
		instruction, err := d.read(path, s.active[path].scope)
		if err == nil && instruction.body == "" {
			delete(s.active, path)
			delete(s.acknowledged, path)
			continue
		}
		if err == nil && total+len(instruction.body) > directoryInstructionTotalLimit {
			err = fmt.Errorf("active instruction budget exceeds %d bytes; instruction %q was not injected or truncated", directoryInstructionTotalLimit, path)
		}
		if err != nil {
			delete(s.acknowledged, path)
			note := "Directory instructions: " + err.Error()
			if !directoryInstructionPresent(messages, note) {
				messages = append(slices.Clone(messages), fantasy.NewUserMessage(note))
			}
			continue
		}
		total += len(instruction.body)
		s.active[path] = instruction
		if !directoryInstructionPresent(messages, instruction.body) {
			messages = append(slices.Clone(messages), fantasy.NewUserMessage(instruction.body))
		}
		s.acknowledged[path] = instruction.hash
	}
	return messages
}

func directoryInstructionPresent(messages []fantasy.Message, body string) bool {
	for _, message := range messages {
		for _, part := range message.Content {
			if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok && strings.Contains(text.Text, body) {
				return true
			}
			if result, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part); ok {
				switch output := result.Output.(type) {
				case fantasy.ToolResultOutputContentText:
					if strings.Contains(output.Text, body) {
						return true
					}
				case fantasy.ToolResultOutputContentError:
					if output.Error != nil && strings.Contains(output.Error.Error(), body) {
						return true
					}
				case fantasy.ToolResultOutputContentMedia:
					if strings.Contains(output.Text, body) {
						return true
					}
				}
			}
		}
	}
	return false
}
