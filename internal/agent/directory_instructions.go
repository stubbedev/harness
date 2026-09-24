package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

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
	// directoryInstructionCacheLimit bounds each of the listing and content
	// caches. Past it a cache is dropped whole and refilled by the lookups
	// that follow, which is cheaper to reason about than an eviction order
	// and only ever costs a round of fresh reads.
	directoryInstructionCacheLimit = 4096
	// directoryInstructionRacyWindow is how old a modification time must be,
	// relative to the moment its directory was listed or its file read,
	// before an unchanged time is taken to mean unchanged content. File
	// systems stamp times from a coarse clock (and some store them in
	// seconds or two), so a change landing in the same tick as the read
	// leaves the time exactly as it was; for a file edited to the same size
	// nothing else would tell the versions apart.
	directoryInstructionRacyWindow = 2 * time.Second
)

var directoryInstructionDefaults = []string{
	".github/copilot-instructions.md", ".cursorrules",
	"CLAUDE.md", "CLAUDE.local.md", "GEMINI.md", "gemini.md",
	"harness.md", "harness.local.md", "Harness.md", "Harness.local.md",
	"HARNESS.md", "HARNESS.local.md", "AGENTS.md", "agents.md", "Agents.md",
}

type DirectoryInstructions struct {
	// mu guards excluded and the sessions. It is shared by every session
	// and sub-agent in the workspace, so no filesystem I/O happens under
	// it: discovery and reads go through cache, whose own lock is held
	// only around map access, and mu is taken afterwards to apply what
	// they found.
	mu    sync.Mutex
	root  string
	names []string
	// parts holds every name split into its path components, and
	// components the set of all of them. A directory listing is reduced
	// to the entries in components before it is cached, so the cache
	// holds a handful of names per directory rather than its contents.
	parts      [][]string
	components map[string]struct{}
	initErr    error
	excluded   map[string][32]byte
	sessions   map[string]*directoryInstructionSession
	cache      directoryInstructionCache
}

// directoryInstructionCache remembers directory listings and instruction
// file contents across lookups. Every tool call touching a path walks the
// directories from it up to the workspace root, and without the cache each
// walk listed every one of them in full and read every file it found.
type directoryInstructionCache struct {
	mu    sync.Mutex
	dirs  map[string]directoryInstructionListing
	files map[string]directoryInstructionFile
}

type directoryInstructionListing struct {
	info    os.FileInfo
	checked time.Time
	present []string
}

type directoryInstructionFile struct {
	info    os.FileInfo
	checked time.Time
	content string
	// instruction is the file rendered for the scope it was last read
	// for, which is the only scope a path is ever found under in practice.
	instruction directoryInstruction
}

type directoryInstructionSession struct {
	active       map[string]directoryInstruction
	acknowledged map[string][32]byte
	// injected remembers, per active path, the index of the message that
	// last carried its instruction text. The per-step re-check looks there
	// first instead of searching the whole history, and falls back to the
	// search when the history was compacted or rewritten underneath it.
	injected map[string]int
}

type directoryInstruction struct {
	scope string
	body  string
	hash  [32]byte
}

type directoryInstructionTool struct {
	toolDecorator
	tracker *DirectoryInstructions
}

func NewDirectoryInstructions(root string, contextPaths []string) *DirectoryInstructions {
	d := &DirectoryInstructions{
		components: make(map[string]struct{}),
		excluded:   make(map[string][32]byte),
		sessions:   make(map[string]*directoryInstructionSession),
		cache: directoryInstructionCache{
			dirs:  make(map[string]directoryInstructionListing),
			files: make(map[string]directoryInstructionFile),
		},
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
	for _, name := range d.names {
		parts := strings.Split(filepath.ToSlash(name), "/")
		d.parts = append(d.parts, parts)
		for _, part := range parts {
			d.components[part] = struct{}{}
		}
	}
	return d
}

func (d *DirectoryInstructions) ExcludePromptPaths(paths []string) {
	if d == nil || d.initErr != nil {
		return
	}
	root, err := os.OpenRoot(d.root)
	if err != nil {
		return
	}
	defer root.Close()
	excluded := make(map[string][32]byte)
	for _, path := range paths {
		joined := filepathext.SmartJoin(d.root, path)
		if resolved, ok := directoryInstructionName(root, d.root, d.root, filepath.ToSlash(path)); ok {
			joined = resolved
		}
		if instruction, err := d.read(root, joined, filepath.Dir(joined)); err == nil && instruction.body != "" {
			excluded[joined] = instruction.hash
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	maps.Copy(d.excluded, excluded)
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
		s = &directoryInstructionSession{
			active:       make(map[string]directoryInstruction),
			acknowledged: make(map[string][32]byte),
			injected:     make(map[string]int),
		}
		d.sessions[id] = s
	}
	return s
}

func (s *directoryInstructionSession) forget(path string) {
	delete(s.active, path)
	delete(s.acknowledged, path)
	delete(s.injected, path)
}

func (d *DirectoryInstructions) activate(ctx context.Context, paths []string) (string, bool, error) {
	if d.initErr != nil {
		return "", false, fmt.Errorf("cannot resolve workspace root: %w", d.initErr)
	}
	instructionRoot, err := os.OpenRoot(d.root)
	if err != nil {
		return "", false, fmt.Errorf("open instruction root: %w", err)
	}
	defer instructionRoot.Close()
	candidates := make(map[string]string)
	// Several paths of one call share most of their ancestors. What a
	// directory holds is looked up once per call and replayed on every
	// later visit, rather than skipped: the scope recorded for a file is
	// the one of its last visit, and a later walk can reach the same file
	// from a deeper directory before arriving at the shallower one again.
	found := make(map[string][]string)
	for _, path := range paths {
		dir, err := d.scope(path)
		if err != nil {
			return "", false, err
		}
		for dir != "" {
			files, ok := found[dir]
			if !ok {
				files = d.discover(instructionRoot, dir)
				found[dir] = files
			}
			for _, file := range files {
				candidates[file] = dir
			}
			if dir == d.root {
				break
			}
			dir = filepath.Dir(dir)
		}
	}
	order := directoryInstructionOrder(candidates)
	read := make([]directoryInstruction, 0, len(order))
	var readErr error
	for _, path := range order {
		if readErr = ctx.Err(); readErr != nil {
			break
		}
		instruction, err := d.read(instructionRoot, path, candidates[path])
		if err != nil {
			readErr = err
			break
		}
		read = append(read, instruction)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.session(ctx)
	loaded := make(map[string]directoryInstruction)
	// The files read before a failure still update the session, as they
	// did when each was applied as soon as it was read.
	for i, instruction := range read {
		path := order[i]
		if instruction.body == "" {
			s.forget(path)
			continue
		}
		if hash, ok := d.excluded[path]; ok && hash == instruction.hash {
			s.forget(path)
			continue
		}
		loaded[path] = instruction
	}
	if readErr != nil {
		return "", false, readErr
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
	resolved, err := filepathext.Resolve(path)
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

// discover returns the configured instruction files present in dir,
// resolved against its listing component by component and requiring exact
// name matches. On case-insensitive filesystems a Stat of every configured
// casing variant would find the same file several times and load its body
// once per variant; matching listing entries keeps discovery exact and the
// resulting path carries the on-disk casing. Every name is matched against
// one listing of dir, where resolving them one at a time listed it once per
// name.
func (d *DirectoryInstructions) discover(root *os.Root, dir string) []string {
	present, ok := d.entries(root, dir)
	if !ok || len(present) == 0 {
		return nil
	}
	var files []string
	for _, parts := range d.parts {
		if !slices.Contains(present, parts[0]) {
			continue
		}
		path := filepath.Join(dir, parts[0])
		for _, part := range parts[1:] {
			nested, ok := d.entries(root, path)
			if !ok || !slices.Contains(nested, part) {
				path = ""
				break
			}
			path = filepath.Join(path, part)
		}
		if path != "" {
			files = append(files, path)
		}
	}
	return files
}

// entries returns the entries of dir that are components of a configured
// instruction name, listing dir only when it changed since it was last
// listed. Adding, removing or renaming an entry updates a directory's
// modification time, so an unchanged time on the same directory means an
// unchanged listing, and a lookup that finds nothing new costs a Stat per
// directory. The listing itself goes through root, which keeps it inside
// the workspace; the Stat only confirms that the directory is still the
// one root listed.
func (d *DirectoryInstructions) entries(root *os.Root, dir string) ([]string, bool) {
	if current, err := os.Stat(dir); err == nil {
		d.cache.mu.Lock()
		cached, ok := d.cache.dirs[dir]
		d.cache.mu.Unlock()
		if ok && directoryInstructionFresh(cached.info, cached.checked, current) {
			return cached.present, true
		}
	}
	rel, inside := filepathext.RelWithin(d.root, dir)
	if !inside {
		return nil, false
	}
	checked := time.Now()
	file, err := root.Open(rel)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	// The directory is stamped before it is read, so an entry created
	// while reading moves its time past the stamp and the next lookup
	// lists it again.
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		return nil, false
	}
	names, err := file.Readdirnames(-1)
	if err != nil {
		return nil, false
	}
	var present []string
	for _, name := range names {
		if _, ok := d.components[name]; ok {
			present = append(present, name)
		}
	}
	d.cache.mu.Lock()
	if len(d.cache.dirs) >= directoryInstructionCacheLimit {
		clear(d.cache.dirs)
	}
	d.cache.dirs[dir] = directoryInstructionListing{info: info, checked: checked, present: present}
	d.cache.mu.Unlock()
	return present, true
}

// directoryInstructionFresh reports whether current describes the same,
// unchanged file or directory that cached did when it was read at checked.
// A modification time inside the racy window of the read is never taken as
// proof: the entry is read again until its time is old enough to be.
func directoryInstructionFresh(cached os.FileInfo, checked time.Time, current os.FileInfo) bool {
	return os.SameFile(cached, current) &&
		cached.ModTime().Equal(current.ModTime()) &&
		cached.Size() == current.Size() &&
		cached.Mode() == current.Mode() &&
		cached.ModTime().Before(checked.Add(-directoryInstructionRacyWindow))
}

// directoryInstructionName resolves a configured instruction name against
// the directory listing of dir the way discover does, without the cache.
// It serves ExcludePromptPaths, whose paths are arbitrary configured
// context paths rather than instruction names, and which runs once.
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

// read loads the instruction file at path, rendered for scope. A file whose
// identity, size and modification time are unchanged since it was last read
// is served from the cache without being opened; anything else is read
// through root, which keeps a symlink from reaching outside the workspace.
// The cache is keyed on what root read, so a symlink retargeted outside
// names a different file, fails the comparison and is refused by root.
func (d *DirectoryInstructions) read(root *os.Root, path, scope string) (directoryInstruction, error) {
	var instruction directoryInstruction
	rel, inside := filepathext.RelWithin(d.root, path)
	if !inside {
		return instruction, nil
	}
	if current, err := os.Stat(path); err == nil {
		d.cache.mu.Lock()
		cached, ok := d.cache.files[path]
		d.cache.mu.Unlock()
		if ok && directoryInstructionFresh(cached.info, cached.checked, current) {
			if cached.instruction.scope == scope {
				return cached.instruction, nil
			}
			return directoryInstructionRender(path, scope, cached.content, cached.instruction.hash), nil
		}
	}
	checked := time.Now()
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
	content := string(body)
	instruction = directoryInstructionRender(path, scope, content, sha256.Sum256(body))
	d.cache.mu.Lock()
	if len(d.cache.files) >= directoryInstructionCacheLimit {
		clear(d.cache.files)
	}
	d.cache.files[path] = directoryInstructionFile{info: info, checked: checked, content: content, instruction: instruction}
	d.cache.mu.Unlock()
	return instruction, nil
}

func directoryInstructionRender(path, scope, content string, hash [32]byte) directoryInstruction {
	return directoryInstruction{
		scope: scope,
		hash:  hash,
		body:  fmt.Sprintf("<directory_instructions path=%q scope=%q version=%x>\nApplies only within this directory scope; deeper instructions take precedence.\n%s\n</directory_instructions>", path, scope, hash, content),
	}
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

// directoryInstructionInjection is text Prepare makes sure the history
// carries, with where it was last seen.
type directoryInstructionInjection struct {
	path string
	text string
	hint int
}

func (d *DirectoryInstructions) Prepare(ctx context.Context, messages []fantasy.Message) []fantasy.Message {
	if d == nil || ctx.Err() != nil {
		return messages
	}
	d.mu.Lock()
	s := d.session(ctx)
	order := directoryInstructionOrder(s.active)
	scopes := make([]string, len(order))
	for i, path := range order {
		scopes[i] = s.active[path].scope
	}
	d.mu.Unlock()
	if len(order) == 0 {
		return messages
	}

	// Re-read every active file outside the lock. The cache makes this a
	// Stat per file unless one changed.
	instructions := make([]directoryInstruction, len(order))
	errs := make([]error, len(order))
	if root, err := os.OpenRoot(d.root); err != nil {
		for i := range errs {
			errs[i] = fmt.Errorf("open instruction root: %w", err)
		}
	} else {
		for i, path := range order {
			instructions[i], errs[i] = d.read(root, path, scopes[i])
		}
		root.Close()
	}

	d.mu.Lock()
	var injections []directoryInstructionInjection
	total := 0
	for i, path := range order {
		// A file dropped by a concurrent call stays dropped.
		if _, ok := s.active[path]; !ok {
			continue
		}
		instruction, err := instructions[i], errs[i]
		if err == nil && instruction.body == "" {
			s.forget(path)
			continue
		}
		if err == nil && total+len(instruction.body) > directoryInstructionTotalLimit {
			err = fmt.Errorf("active instruction budget exceeds %d bytes; instruction %q was not injected or truncated", directoryInstructionTotalLimit, path)
		}
		hint, ok := s.injected[path]
		if !ok {
			hint = -1
		}
		if err != nil {
			delete(s.acknowledged, path)
			injections = append(injections, directoryInstructionInjection{path: path, text: "Directory instructions: " + err.Error(), hint: hint})
			continue
		}
		total += len(instruction.body)
		s.active[path] = instruction
		injections = append(injections, directoryInstructionInjection{path: path, text: instruction.body, hint: hint})
		s.acknowledged[path] = instruction.hash
	}
	d.mu.Unlock()

	// The history is searched outside the lock too; only the positions
	// found are written back.
	cloned := false
	for i := range injections {
		injection := &injections[i]
		injection.hint = directoryInstructionIndex(messages, injection.text, injection.hint)
		if injection.hint >= 0 {
			continue
		}
		if !cloned {
			messages = slices.Clone(messages)
			cloned = true
		}
		injection.hint = len(messages)
		messages = append(messages, fantasy.NewUserMessage(injection.text))
	}
	d.mu.Lock()
	for _, injection := range injections {
		if _, ok := s.active[injection.path]; ok {
			s.injected[injection.path] = injection.hint
		}
	}
	d.mu.Unlock()
	return messages
}

func directoryInstructionPresent(messages []fantasy.Message, body string) bool {
	return directoryInstructionIndex(messages, body, -1) >= 0
}

// directoryInstructionIndex returns the index of the first message carrying
// body, or -1. The message at hint is checked first, which is where the body
// was found or injected on the previous step, so a history that only grew
// since then is not searched at all.
func directoryInstructionIndex(messages []fantasy.Message, body string, hint int) int {
	if hint >= 0 && hint < len(messages) && directoryInstructionCarries(messages[hint], body) {
		return hint
	}
	for i, message := range messages {
		if directoryInstructionCarries(message, body) {
			return i
		}
	}
	return -1
}

func directoryInstructionCarries(message fantasy.Message, body string) bool {
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
	return false
}
