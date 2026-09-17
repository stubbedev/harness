package model

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/dialog"
)

func TestModelSelectionQueuedWhileAgentBusy(t *testing.T) {
	t.Parallel()
	u := newFrameTestUI(t)
	u.agentBusyCache.set(true)

	cmd := u.handleSelectModel(dialog.ActionSelectModel{
		Model:     config.SelectedModel{Provider: "openai", Model: "gpt-5.3"},
		ModelType: config.SelectedModelTypeLarge,
	})

	require.NotNil(t, u.pendingModelAction, "a mid-turn model choice must be queued, not dropped")
	_, ok := u.pendingModelAction.(dialog.ActionSelectModel)
	require.True(t, ok)
	require.NotNil(t, cmd, "the user must be told the choice applies after the turn")
}

func TestReasoningEffortQueuedWhileAgentBusy(t *testing.T) {
	t.Parallel()
	u := newFrameTestUI(t)
	u.agentBusyCache.set(true)

	cmd := u.handleSelectReasoningEffort(dialog.ActionSelectReasoningEffort{Effort: "low"})

	require.Equal(t, dialog.ActionSelectReasoningEffort{Effort: "low"}, u.pendingModelAction)
	require.NotNil(t, cmd, "the user must be told the choice applies after the turn")
}

func TestApplyPendingModelActionWithoutPending(t *testing.T) {
	t.Parallel()
	u := newFrameTestUI(t)

	require.Nil(t, u.applyPendingModelAction())
}
