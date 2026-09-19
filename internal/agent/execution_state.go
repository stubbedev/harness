package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"

	"github.com/stubbedev/harness/internal/message"
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
)

type executionEntry struct {
	Key      string          `json:"key"`
	Tool     string          `json:"tool,omitempty"`
	Input    string          `json:"input,omitempty"`
	Status   string          `json:"status,omitempty"`
	ExitCode *int            `json:"exit_code,omitempty"`
	Detail   string          `json:"detail,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	Sequence uint64          `json:"sequence"`
}

type executionState struct {
	Sequence     uint64            `json:"sequence,omitempty"`
	Files        []executionEntry  `json:"changed_files,omitempty"`
	Commands     []executionEntry  `json:"commands,omitempty"`
	Sessions     []executionEntry  `json:"shell_sessions,omitempty"`
	Failures     []executionEntry  `json:"outstanding_failures,omitempty"`
	Jobs         []executionEntry  `json:"subagent_jobs,omitempty"`
	Verification []executionEntry  `json:"verification,omitempty"`
	Omitted      map[string]uint64 `json:"omitted,omitempty"`
	calls        map[string]message.ToolCall
	seen         map[string]string
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
	if len(text) <= limit {
		return text
	}
	for limit > 0 && (text[limit]&0xc0) == 0x80 {
		limit--
	}
	return text[:limit] + "…"
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
	return hex.EncodeToString(digest[:])
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

func (s *executionState) Ingest(msgs []message.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls == nil {
		s.calls = make(map[string]message.ToolCall)
	}
	if s.seen == nil {
		s.seen = make(map[string]string)
	}
	for _, msg := range msgs {
		for _, part := range msg.Parts {
			switch part := part.(type) {
			case message.ToolCall:
				s.calls[part.ID] = part
			case message.ToolResult:
				encoded, _ := json.Marshal(part)
				fingerprint := executionKey("result", string(encoded))
				id := part.ToolCallID
				if id == "" {
					id = fingerprint
				}
				if s.seen[id] == fingerprint {
					continue
				}
				s.seen[id] = fingerprint
				s.Sequence++
				call := s.calls[part.ToolCallID]
				if call.Name == "" {
					call.Name = part.Name
				}
				s.ingestResult(call, part)
			}
		}
	}
	s.bound()
	if len(s.calls) > 512 {
		s.calls = make(map[string]message.ToolCall)
	}
	if len(s.seen) > 1024 {
		s.seen = make(map[string]string)
	}
}

func (s *executionState) ingestResult(call message.ToolCall, result message.ToolResult) {
	key := executionKey(call.Name, call.Input)
	if call.Input == "" {
		key = executionKey(call.Name, result.ToolCallID)
	}
	entry := executionEntry{Key: key, Tool: call.Name, Input: executionClip(call.Input, executionTextLimit)}
	failed, success := result.IsError, !result.IsError
	var metadata map[string]json.RawMessage
	_ = json.Unmarshal([]byte(result.Metadata), &metadata)
	if call.Name == "shell" {
		failed, success, entry = s.ingestShell(call, result, entry)
	}
	var failedEdits []json.RawMessage
	if json.Unmarshal(metadata["edits_failed"], &failedEdits) == nil && len(failedEdits) > 0 {
		failed, success = true, false
	}
	if call.Name == "verify" || call.Name == "verification" {
		var verification struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(metadata["verification"], &verification) == nil {
			failed = failed || verification.Status == "failed" || verification.Status == "blocked"
			success = !failed && verification.Status == "passed"
		}
	}
	if failed {
		entry.Detail = executionClip(result.Content, executionTextLimit)
		entry.Status = "failed"
		s.put(&s.Failures, entry)
	} else if success {
		s.clearFailure(entry.Key)
	}
	if !result.IsError && (call.Name == "write" || call.Name == "edit") && metadata["changed_files"] == nil && metadata["file_mutations"] == nil && metadata["mutations"] == nil {
		var params struct {
			FilePath string `json:"file_path"`
		}
		var applied int
		_ = json.Unmarshal(metadata["edits_applied"], &applied)
		if json.Unmarshal([]byte(call.Input), &params) == nil && params.FilePath != "" && (call.Name == "write" || applied > 0) {
			s.put(&s.Files, executionEntry{Key: params.FilePath, Tool: call.Name, Status: "changed", Metadata: json.RawMessage(`{"version_unavailable":true}`)})
		}
	}
	if len(metadata) == 0 {
		return
	}
	s.ingestFiles(metadata, entry)
	if call.Name == "verification" || call.Name == "verify" {
		entry.Metadata = compactVerificationMetadata(metadata)
		entry.Status = "reported"
		var verification struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(metadata["verification"], &verification) == nil && verification.Status != "" {
			entry.Status = verification.Status
		}
		s.put(&s.Verification, entry)
	}
	if call.Name == "agent" {
		var jobs []backgroundJobMetadata
		if json.Unmarshal(metadata["jobs"], &jobs) == nil && len(jobs) > 0 {
			for _, job := range jobs {
				data, _ := json.Marshal(job)
				s.put(&s.Jobs, executionEntry{Key: job.Handle, Tool: call.Name, Status: job.Status, Metadata: data})
			}
			return
		}
		entry.Metadata = boundedExecutionMetadata(json.RawMessage(result.Metadata))
		entry.Status = "reported"
		for _, field := range []string{"handle", "job_id", "id"} {
			var id string
			if json.Unmarshal(metadata[field], &id) == nil && id != "" {
				entry.Key = id
				break
			}
		}
		s.put(&s.Jobs, entry)
	}
}

func (s *executionState) ingestFiles(metadata map[string]json.RawMessage, entry executionEntry) {
	for _, field := range []string{"changed_files", "file_mutations", "mutations"} {
		var files []map[string]json.RawMessage
		if json.Unmarshal(metadata[field], &files) != nil {
			continue
		}
		for _, file := range files {
			var path string
			for _, name := range []string{"path", "file_path"} {
				_ = json.Unmarshal(file[name], &path)
				if path != "" {
					break
				}
			}
			if path == "" {
				continue
			}
			data, _ := json.Marshal(file)
			fileEntry := executionEntry{Key: path, Tool: entry.Tool, Status: "changed", Metadata: boundedExecutionMetadata(data)}
			s.put(&s.Files, fileEntry)
		}
	}
}

func (s *executionState) ingestShell(call message.ToolCall, result message.ToolResult, entry executionEntry) (bool, bool, executionEntry) {
	var params struct {
		Command string `json:"command"`
		Session string `json:"session"`
		Reset   bool   `json:"reset"`
	}
	var meta struct {
		Session       string `json:"session"`
		ExitCode      *int   `json:"exit_code"`
		Running       bool   `json:"running"`
		Waiting       bool   `json:"waiting"`
		Queued        bool   `json:"queued"`
		WhileBusy     bool   `json:"while_busy"`
		AltScreen     bool   `json:"alt_screen"`
		Interrupted   bool   `json:"interrupted"`
		ShellExited   bool   `json:"shell_exited"`
		ShellExitCode *int   `json:"shell_exit_code"`
	}
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
	entry.Status = "unknown"
	switch {
	case result.IsError:
		entry.Status = "failed"
	case params.Reset:
		entry.Status = "reset"
	case meta.Interrupted:
		entry.Status = "interrupted"
	case meta.Queued:
		entry.Status = "queued"
	case meta.Waiting:
		entry.Status = "waiting"
	case meta.Running || meta.WhileBusy || meta.AltScreen:
		entry.Status = "running"
	case meta.ExitCode != nil && *meta.ExitCode != 0:
		entry.Status = "failed"
	case meta.ExitCode != nil:
		entry.Status = "succeeded"
	case meta.ShellExited:
		entry.Status = "shell_exited"
		entry.ExitCode = meta.ShellExitCode
	}
	s.put(&s.Commands, entry)
	if !result.IsError && !meta.Queued && !meta.WhileBusy {
		sessionEntry := entry
		sessionEntry.Key = name
		sessionEntry.Detail = entry.Key
		s.put(&s.Sessions, sessionEntry)
	}
	return result.IsError || entry.Status == "failed" || (entry.Status == "shell_exited" && entry.ExitCode != nil && *entry.ExitCode != 0) || entry.Status == "interrupted", entry.Status == "succeeded", entry
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
