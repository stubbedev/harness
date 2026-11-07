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
	"github.com/stubbedev/harness/internal/proto"
	"github.com/stubbedev/harness/internal/pubsub"
)

// TestE2E_StalledStreamIsDisconnected: a client that keeps its event
// stream open but stops reading it is disconnected once a write to it
// stalls past the write timeout, while the other client keeps receiving.
func TestE2E_StalledStreamIsDisconnected(t *testing.T) {
	t.Parallel()
	h := newE2EHarness(t)
	// The reading client has to take every frame within this too, on a
	// runner busy with the rest of the package under the race detector,
	// so it is a second rather than the shortest a stall could be caught.
	h.srv.eventWriteTimeout = time.Second

	stalledID := proto.ClientID(uuid.New().String())
	t.Logf("stalled=%s", stalledID)
	q := url.Values{"client_id": []string{string(stalledID)}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		h.httpSrv.URL+"/v1/workspaces/"+h.workspace.ID+"/events?"+q.Encode(), nil)
	require.NoError(t, err)
	stalled, err := h.httpSrv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = stalled.Body.Close() })
	require.Equal(t, http.StatusOK, stalled.StatusCode)

	readingID := proto.ClientID(uuid.New().String())
	t.Logf("reading=%s", readingID)
	ctxB, cancelB := context.WithCancel(t.Context())
	t.Cleanup(cancelB)
	evcB, killB := h.subscribeSSE(t, ctxB, h.workspace.ID, string(readingID))
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
		return backend.ClientLiveStreamsForTest(h.workspace, stalledID) == 0
	}, 15*time.Second, 10*time.Millisecond, "the stalled stream must be disconnected")
	require.Equal(t, 1, backend.ClientLiveStreamsForTest(h.workspace, readingID),
		"the client that keeps reading must keep its stream")
}
