package agent

import "charm.land/fantasy"

type backgroundJobMetadata struct {
	Handle    string `json:"handle"`
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	Status    string `json:"status"`
}

func withBackgroundMetadata(response fantasy.ToolResponse, runs []*backgroundRun) fantasy.ToolResponse {
	jobs := make([]backgroundJobMetadata, 0, len(runs))
	for _, run := range runs {
		finished, status, _, _ := run.snapshot()
		if !finished {
			status = "running"
		}
		jobs = append(jobs, backgroundJobMetadata{Handle: run.handle, SessionID: run.childSession, Agent: run.agentName, Status: status})
	}
	if len(jobs) == 0 {
		return response
	}
	return fantasy.WithResponseMetadata(response, struct {
		Jobs []backgroundJobMetadata `json:"jobs"`
	}{Jobs: jobs})
}
