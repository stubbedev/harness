package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/backend"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/pubsub"
)

// TestE2E_StalledStreamIsDisconnected: a client that keeps its event
// stream open but stops reading it is disconnected once a write to it
// stalls past the write timeout, while the other client keeps receiving.
func TestE2E_StalledStreamIsDisconnected(t *testing.T) {
	t.Parallel()
	h := newE2EHarness(t)
	h.srv.eventWriteTimeout = 100 * time.Millisecond

	q := url.Values{"client_id": []string{uuid.New().String()}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		h.httpSrv.URL+"/v1/workspaces/"+h.workspace.ID+"/events?"+q.Encode(), nil)
	require.NoError(t, err)
	stalled, err := h.httpSrv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = stalled.Body.Close() })
	require.Equal(t, http.StatusOK, stalled.StatusCode)

	ctxB, cancelB := context.WithCancel(t.Context())
	t.Cleanup(cancelB)
	evcB, killB := h.subscribeSSE(t, ctxB, h.workspace.ID, uuid.New().String())
	t.Cleanup(killB)
	go func() {
		for range evcB {
		}
	}()
	h.waitForAttached(t, 2)

	// How much a stalled loopback socket absorbs before a write blocks is
	// the OS's choice; Windows autotunes it to tens of megabytes. So events
	// keep coming until the stream is dropped, capped below the frame
	// broker's per-subscriber buffer so that only the write timeout, never
	// eviction of a full subscriber, can be what disconnects it.
	const maxEvents = 2048
	text := strings.Repeat("x", 64<<10)
	sent := 0
	require.Eventually(t, func() bool {
		for n := 0; n < 16 && sent < maxEvents; n++ {
			h.app.SendEvent(pubsub.Event[message.Message]{
				Type: pubsub.CreatedEvent,
				Payload: message.Message{
					ID:        fmt.Sprintf("m-%d", sent),
					SessionID: "s",
					Role:      message.Assistant,
					Parts:     []message.ContentPart{message.TextContent{Text: text}},
				},
			})
			sent++
		}
		return backend.WorkspaceLiveStreamCountForTest(h.workspace) == 1
	}, 10*time.Second, 10*time.Millisecond, "the stalled stream must be disconnected")
}
