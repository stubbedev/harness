package prompt

import "fmt"

// Context files ride in the system prompt on every request, so their
// size is paid for on every turn. A project that keeps a long AGENTS.md,
// or points context_paths at a directory of notes, would otherwise spend
// the window on prose before the conversation starts. Each file is cut
// at maxContextFileBytes and the whole set at maxContextTotalBytes, at
// line boundaries, with a marker saying what was dropped so the model
// knows to read the file itself when it matters.
const (
	maxContextFileBytes  = 32 << 10
	maxContextTotalBytes = 96 << 10
)

// truncateContextFile cuts one file's content to limit bytes at a line
// boundary and appends a marker naming the file and the bytes dropped.
// Content within the limit is returned as is.
func truncateContextFile(f ContextFile, limit int) ContextFile {
	if len(f.Content) <= limit {
		return f
	}
	cut := limit
	for cut > 0 && f.Content[cut-1] != '\n' {
		cut--
	}
	if cut == 0 {
		// A single line longer than the limit: cut it mid-line rather
		// than dropping everything.
		cut = limit
	}
	dropped := len(f.Content) - cut
	f.Content = f.Content[:cut] + fmt.Sprintf("\n[context file %s truncated: %d bytes not shown; read the file for the rest]\n", f.Path, dropped)
	return f
}

// capContextFiles applies the per-file limit to every file and the total
// limit across them, in order: once the total is spent, later files are
// cut to what is left, and a file with no room left becomes its marker
// alone.
func capContextFiles(files []ContextFile, perFile, total int) []ContextFile {
	out := make([]ContextFile, 0, len(files))
	remaining := total
	for _, f := range files {
		limit := min(perFile, max(remaining, 0))
		f = truncateContextFile(f, limit)
		remaining -= len(f.Content)
		out = append(out, f)
	}
	return out
}
