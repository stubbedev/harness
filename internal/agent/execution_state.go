package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash/maphash"
	"strconv"
	"strings"
	"sync"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/toolname"
	"github.com/stubbedev/harness/internal/verification"

	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/stringext"
)

const (
	executionStateVersion  = 1
	executionFilesLimit    = 64
	executionCommandsLimit = 24
	executionSessionsLimit = 16
	executionFailuresLimit = 32
	executionRecordsLimit  = 16
	executionTextLimit     = 512
	executionMetadataLimit = 2048
	executionKeyLength     = 12
)

type executionEntry struct {
	Key      string          `json:"key"`
	Tool     string          `json:"tool,omitempty"`
	Input    string          `json:"input,omitempty"`
	Status   string          `json:"status,omitempty"`
	ExitCode *int            `json:"exit_code,omitempty"`
	Detail   string          `json:"detail,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	// Sequence orders entries while the state is built; it is not
	// rendered, since the model gets nothing from a counter per entry.
	Sequence uint64 `json:"-"`
}

type executionState struct {
	Sequence     uint64            `json:"-"`
	Files        []executionEntry  `json:"changed_files,omitempty"`
	Commands     []executionEntry  `json:"commands,omitempty"`
	Sessions     []executionEntry  `json:"shell_sessions,omitempty"`
	Failures     []executionEntry  `json:"outstanding_failures,omitempty"`
	Jobs         []executionEntry  `json:"subagent_jobs,omitempty"`
	Verification []executionEntry  `json:"verification,omitempty"`
	Omitted      map[string]uint64 `json:"omitted,omitempty"`
	calls        map[string]message.ToolCall
	seen         map[string]uint64
	mu           sync.Mutex
}

type executionEnvelope struct {
	Version   int             `json:"harness_execution_version"`
	Narrative string          `json:"narrative"`
	State     *executionState `json:"execution_state"`
}

func splitExecutionSummary(summary string) (string, *executionState) {
	var envelope executionEnvelope
	if json.Unmarshal([]byte(summary), &envelope) == nil && envelope.Version == executionStateVersion && envelope.State != nil {
		envelope.State.bound()
		return envelope.Narrative, envelope.State
	}
	return summary, &executionState{}
}

func newExecutionState(summary string) *executionState {
	_, state := splitExecutionSummary(summary)
	return state
}

func (s *executionState) useful() bool {
	return len(s.Files)+len(s.Commands)+len(s.Sessions)+len(s.Failures)+len(s.Jobs)+len(s.Verification)+len(s.Omitted) > 0
}

func (s *executionState) Render() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.useful() {
		return ""
	}
	s.bound()
	data, _ := json.Marshal(s)
	return "<execution_state>\n" + string(data) + "\n</execution_state>"
}

func (s *executionState) Summary(narrative string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.useful() {
		return narrative
	}
	s.bound()
	data, _ := json.Marshal(executionEnvelope{Version: executionStateVersion, Narrative: narrative, State: s})
	return string(data)
}

func renderExecutionSummary(summary string) string {
	narrative, state := splitExecutionSummary(summary)
	if snapshot := state.Render(); snapshot != "" {
		return narrative + "\n\n" + snapshot
	}
	return narrative
}

func executionClip(text string, limit int) string {
	return stringext.Truncate(text, limit+1, "…")
}

func executionKey(tool, input string) string {
	var value any
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.UseNumber()
	if decoder.Decode(&value) == nil {
		canonical, err := json.Marshal(value)
		if err == nil {
			input = string(canonical)
		}
	}
	digest := sha256.Sum256([]byte(tool + "\x00" + input))
	// Twelve hex digits: the key only has to be unique within one
	// session, and the full digest was a third of every snapshot.
	return hex.EncodeToString(digest[:])[:executionKeyLength]
}

func (s *executionState) put(entries *[]executionEntry, entry executionEntry) {
	for i := range *entries {
		if (*entries)[i].Key == entry.Key {
			*entries = append((*entries)[:i], (*entries)[i+1:]...)
			break
		}
	}
	entry.Sequence = s.Sequence
	*entries = append(*entries, entry)
}

func (s *executionState) clearFailure(key string) {
	for i := range s.Failures {
		if s.Failures[i].Key == key {
			s.Failures = append(s.Failures[:i], s.Failures[i+1:]...)
			return
		}
	}
}

func (s *executionState) bound() {
	for _, group := range []struct {
		name    string
		entries *[]executionEntry
		limit   int
	}{
		{"changed_files", &s.Files, executionFilesLimit},
		{"commands", &s.Commands, executionCommandsLimit},
		{"shell_sessions", &s.Sessions, executionSessionsLimit},
		{"outstanding_failures", &s.Failures, executionFailuresLimit},
		{"subagent_jobs", &s.Jobs, executionRecordsLimit},
		{"verification", &s.Verification, executionRecordsLimit},
	} {
		if extra := len(*group.entries) - group.limit; extra > 0 {
			if s.Omitted == nil {
				s.Omitted = make(map[string]uint64)
			}
			s.Omitted[group.name] += uint64(extra)
			*group.entries = (*group.entries)[extra:]
		}
		for i := range *group.entries {
			entry := &(*group.entries)[i]
			entry.Key = executionClip(entry.Key, executionTextLimit)
			entry.Input = executionClip(entry.Input, executionTextLimit)
			entry.Detail = executionClip(entry.Detail, executionTextLimit)
			entry.Tool = executionClip(entry.Tool, 80)
			entry.Status = executionClip(entry.Status, 80)
			entry.Metadata = boundedExecutionMetadata(entry.Metadata)
		}
	}
	for {
		data, _ := json.Marshal(s)
		if len(data) <= 32768 {
			break
		}
		removed := false
		for _, group := range []struct {
			name    string
			entries *[]executionEntry
		}{
			{"commands", &s.Commands},
			{"changed_files", &s.Files},
			{"verification", &s.Verification},
			{"subagent_jobs", &s.Jobs},
			{"shell_sessions", &s.Sessions},
			{"outstanding_failures", &s.Failures},
		} {
			if len(*group.entries) == 0 {
				continue
			}
			*group.entries = (*group.entries)[1:]
			if s.Omitted == nil {
				s.Omitted = make(map[string]uint64)
			}
			s.Omitted[group.name]++
			removed = true
			break
		}
		if !removed {
			break
		}
	}
}

func boundedExecutionMetadata(data json.RawMessage) json.RawMessage {
	if len(data) == 0 || !json.Valid(data) {
		return nil
	}
	if len(data) <= executionMetadataLimit {
		return data
	}
	digest := sha256.Sum256(data)
	bounded, _ := json.Marshal(map[string]any{"omitted_bytes": len(data), "sha256": hex.EncodeToString(digest[:])})
	return bounded
}

// executionSeed seeds resultFingerprint. The fingerprints only live in
// the in-memory seen map, never in a summary, so a seed that changes
// with every process is fine.
var executionSeed = maphash.MakeSeed()

// resultFingerprint tells Ingest whether it has taken in a result
// already. The tool call ID is the identity; the fingerprint beside it
// covers what the state is built from, so a provider that reuses IDs
// across steps still has each outcome taken in. The body is left out
// on purpose, and not only because hashing every image and every large
// output again on each turn cost milliseconds: history aging and
// deduplication rewrite old bodies into stubs under the same ID, and
// taking a rewritten result in again would put the stub in as a failure
// detail, or bring back a failure a later result had cleared. A result
// without an ID has nothing else to be known by, so its body is hashed.
func resultFingerprint(result message.ToolResult) uint64 {
	var h maphash.Hash
	h.SetSeed(executionSeed)
	var flags byte
	if result.IsError {
		flags |= 1
	}
	if result.Canceled {
		flags |= 2
	}
	_, _ = h.WriteString(result.Name)
	_ = h.WriteByte(0)
	_, _ = h.WriteString(result.Metadata)
	_ = h.WriteByte(flags)
	if result.ToolCallID == "" {
		_, _ = h.WriteString(result.Content)
		_ = h.WriteByte(0)
		_, _ = h.WriteString(result.MIMEType)
		_ = h.WriteByte(0)
		_, _ = h.WriteString(result.Data)
	}
	return h.Sum64()
}

func (s *executionState) Ingest(msgs []message.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls == nil {
		s.calls = make(map[string]message.ToolCall)
	}
	if s.seen == nil {
		s.seen = make(map[string]uint64)
	}
	changed := false
	for _, msg := range msgs {
		for _, part := range msg.Parts {
			switch part := part.(type) {
			case message.ToolCall:
				s.calls[part.ID] = part
			case message.ToolResult:
				fingerprint := resultFingerprint(part)
				id := part.ToolCallID
				if id == "" {
					// A NUL cannot start a provider's ID, so this key
					// never lands on a real one.
					id = "\x00" + strconv.FormatUint(fingerprint, 16)
				}
				if seen, ok := s.seen[id]; ok && seen == fingerprint {
					continue
				}
				s.seen[id] = fingerprint
				s.Sequence++
				changed = true
				call := s.calls[part.ToolCallID]
				if call.Name == "" {
					call.Name = part.Name
				}
				s.ingestResult(call, part)
			}
		}
	}
	// Bounding encodes the whole state to measure it, and a replayed
	// history or a lone tool call changes nothing that needs it.
	if changed {
		s.bound()
	}
	if len(s.calls) > 512 {
		s.calls = make(map[string]message.ToolCall)
	}
	if len(s.seen) > 1024 {
		s.seen = make(map[string]uint64)
	}
}

// The statuses an execution entry records. They reach the model in the
// execution-state note, so they are its vocabulary for how a call ended.
const (
	executionUnknown     = "unknown"
	executionFailed      = "failed"
	executionSucceeded   = "succeeded"
	executionReset       = "reset"
	executionInterrupted = "interrupted"
	executionQueued      = "queued"
	executionWaiting     = "waiting"
	executionRunning     = "running"
	executionShellExited = "shell_exited"
	executionChanged     = "changed"
	executionReported    = "reported"
)

func (s *executionState) ingestResult(call message.ToolCall, result message.ToolResult) {
	key := executionKey(call.Name, call.Input)
	if call.Input == "" {
		key = executionKey(call.Name, result.ToolCallID)
	}
	entry := executionEntry{Key: key, Tool: call.Name, Input: executionClip(call.Input, executionTextLimit)}
	failed, success := result.IsError, !result.IsError
	var common tools.ExecutionMetadata
	_ = json.Unmarshal([]byte(result.Metadata), &common)
	if call.Name == toolname.Shell {
		failed, success, entry = s.ingestShell(call, result, entry)
	}
	if len(common.EditsFailed) > 0 {
		failed, success = true, false
	}
	var verified struct {
		Verification *verification.Result `json:"verification"`
	}
	if call.Name == toolname.Verify && json.Unmarshal([]byte(result.Metadata), &verified) == nil && verified.Verification != nil {
		status := verified.Verification.Status
		failed = failed || status == verification.Failed || status == verification.Blocked
		success = !failed && status == verification.Passed
	}
	if failed {
		entry.Detail = executionClip(result.Content, executionTextLimit)
		entry.Status = executionFailed
		s.put(&s.Failures, entry)
	} else if success {
		s.clearFailure(entry.Key)
	}
	if !result.IsError && (call.Name == toolname.Write || call.Name == toolname.Edit) && len(common.FileMutations) == 0 {
		var params struct {
			FilePath string `json:"file_path"`
		}
		if json.Unmarshal([]byte(call.Input), &params) == nil && params.FilePath != "" && (call.Name == toolname.Write || common.EditsApplied > 0) {
			s.put(&s.Files, executionEntry{Key: params.FilePath, Tool: call.Name, Status: executionChanged, Metadata: json.RawMessage(`{"version_unavailable":true}`)})
		}
	}
	if result.Metadata == "" {
		return
	}
	s.ingestFiles(common.FileMutations, entry)
	if call.Name == toolname.Verify {
		var metadata map[string]json.RawMessage
		_ = json.Unmarshal([]byte(result.Metadata), &metadata)
		entry.Metadata = compactVerificationMetadata(metadata)
		entry.Status = executionReported
		if verified.Verification != nil && verified.Verification.Status != "" {
			entry.Status = string(verified.Verification.Status)
		}
		s.put(&s.Verification, entry)
	}
	if call.Name == toolname.Agent {
		var dispatched struct {
			Jobs []backgroundJobMetadata `json:"jobs"`
		}
		if json.Unmarshal([]byte(result.Metadata), &dispatched) == nil && len(dispatched.Jobs) > 0 {
			for _, job := range dispatched.Jobs {
				data, _ := json.Marshal(job)
				s.put(&s.Jobs, executionEntry{Key: job.Handle, Tool: call.Name, Status: string(job.Status), Metadata: data})
			}
			return
		}
		entry.Metadata = boundedExecutionMetadata(json.RawMessage(result.Metadata))
		entry.Status = executionReported
		var handle struct {
			Handle string `json:"handle"`
		}
		if json.Unmarshal([]byte(result.Metadata), &handle) == nil && handle.Handle != "" {
			entry.Key = handle.Handle
		}
		s.put(&s.Jobs, entry)
	}
}

func (s *executionState) ingestFiles(mutations []tools.FileMutation, entry executionEntry) {
	for _, mutation := range mutations {
		if mutation.Path == "" {
			continue
		}
		// The key is the path; saying it again in the metadata was the
		// longest field of every file entry.
		data, _ := json.Marshal(struct {
			Version string `json:"version"`
		}{mutation.Version})
		s.put(&s.Files, executionEntry{Key: mutation.Path, Tool: entry.Tool, Status: executionChanged, Metadata: boundedExecutionMetadata(data)})
	}
}

func (s *executionState) ingestShell(call message.ToolCall, result message.ToolResult, entry executionEntry) (bool, bool, executionEntry) {
	var params tools.ShellParams
	var meta tools.ShellResponseMetadata
	_ = json.Unmarshal([]byte(call.Input), &params)
	_ = json.Unmarshal([]byte(result.Metadata), &meta)
	name := meta.Session
	if name == "" {
		name = params.Session
	}
	if name == "" {
		name = "main"
	}
	entry.Input = executionClip(params.Command, executionTextLimit)
	var previous *executionEntry
	for i := range s.Sessions {
		if s.Sessions[i].Key == name {
			previous = &s.Sessions[i]
			break
		}
	}
	if params.Command == "" && !params.Reset && previous != nil && previous.Detail != "" {
		entry.Input = previous.Input
		entry.Key = previous.Detail
	}
	entry.ExitCode = meta.ExitCode
	entry.Status = executionUnknown
	switch {
	case result.IsError:
		entry.Status = executionFailed
	case params.Reset:
		entry.Status = executionReset
	case meta.Interrupted:
		entry.Status = executionInterrupted
	case meta.Queued:
		entry.Status = executionQueued
	case meta.Waiting:
		entry.Status = executionWaiting
	case meta.Running || meta.WhileBusy || meta.AltScreen:
		entry.Status = executionRunning
	case meta.ExitCode != nil && *meta.ExitCode != 0:
		entry.Status = executionFailed
	case meta.ExitCode != nil:
		entry.Status = executionSucceeded
	case meta.ShellExited:
		entry.Status = executionShellExited
		entry.ExitCode = meta.ShellExitCode
	}
	s.put(&s.Commands, entry)
	if !result.IsError && !meta.Queued && !meta.WhileBusy {
		sessionEntry := entry
		sessionEntry.Key = name
		sessionEntry.Detail = entry.Key
		s.put(&s.Sessions, sessionEntry)
	}
	return result.IsError || entry.Status == executionFailed || (entry.Status == executionShellExited && entry.ExitCode != nil && *entry.ExitCode != 0) || entry.Status == executionInterrupted, entry.Status == executionSucceeded, entry
}

func compactVerificationMetadata(metadata map[string]json.RawMessage) json.RawMessage {
	var verification map[string]json.RawMessage
	if json.Unmarshal(metadata["verification"], &verification) != nil {
		data, _ := json.Marshal(metadata)
		return boundedExecutionMetadata(data)
	}
	var checks []map[string]json.RawMessage
	if json.Unmarshal(verification["checks"], &checks) == nil {
		for _, check := range checks {
			delete(check, "output")
			delete(check, "duration_ns")
		}
		if len(checks) > executionRecordsLimit {
			verification["omitted_checks"], _ = json.Marshal(len(checks) - executionRecordsLimit)
			checks = checks[:executionRecordsLimit]
		}
		verification["checks"], _ = json.Marshal(checks)
	}
	data, _ := json.Marshal(verification)
	if len(data) > executionMetadataLimit {
		delete(verification, "changed_paths")
		delete(verification, "checks")
		verification["details_omitted"] = json.RawMessage("true")
		data, _ = json.Marshal(verification)
	}
	return boundedExecutionMetadata(data)
}
