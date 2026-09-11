package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"charm.land/x/vcr"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v4"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
)

// restamp rewrites the recorded request bodies from the current system prompts
// and tool definitions, replaying the recorded responses as-is. Use it after
// editing a prompt template or a tool description, when the model's side of the
// conversation is unchanged and only our side of it drifted:
//
//	go test ./internal/agent -run TestCoderAgent -restamp
//
// It needs no API key. Re-record with `just record` instead whenever the change
// should alter what the model does.
var restamp = flag.Bool("restamp", false, "rewrite VCR cassette request bodies from the current prompts and tool definitions")

// testRecorder is the subset of *vcr.Recorder the agent tests rely on, so that
// restamp runs can substitute their own transport.
type testRecorder interface {
	http.RoundTripper
	GetDefaultClient() *http.Client
}

func newTestRecorder(t *testing.T) testRecorder {
	if *restamp {
		return newRestampRecorder(t)
	}
	inner := vcr.NewRecorder(t)
	os.MkdirAll("/tmp/vcrlog", 0o755)
	n := 0
	return &loggingRecorder{inner: inner, t: t, n: &n}
}

type loggingRecorder struct {
	inner testRecorder
	t     *testing.T
	n     *int
}

func (l *loggingRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	i := *l.n
	*l.n++
	var body []byte
	if req.Body != nil && req.Body != http.NoBody {
		body, _ = io.ReadAll(req.Body)
		req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	os.WriteFile(fmt.Sprintf("/tmp/vcrlog/%s-req%d.json", l.t.Name(), i), body, 0o644)
	resp, err := l.inner.RoundTrip(req)
	if resp != nil && resp.Body != nil {
		rb, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		os.WriteFile(fmt.Sprintf("/tmp/vcrlog/%s-req%d-resp.txt", l.t.Name(), i), rb, 0o644)
		resp.Body = io.NopCloser(bytes.NewReader(rb))
	}
	return resp, err
}

func (l *loggingRecorder) GetDefaultClient() *http.Client {
	return l.inner.GetDefaultClient()
}

// restampRecorder replays a cassette positionally, pairing each outgoing
// request with the next unused interaction for the same method and URL, and
// overwrites that interaction's recorded request with the outgoing one.
type restampRecorder struct {
	t    *testing.T
	cas  *cassette.Cassette
	mu   sync.Mutex
	used []bool
}

func newRestampRecorder(t *testing.T) *restampRecorder {
	cas, err := cassette.Load(filepath.Join("testdata", t.Name()))
	require.NoError(t, err, "restamp: load cassette")
	cas.MarshalFunc = restampMarshal

	r := &restampRecorder{t: t, cas: cas, used: make([]bool, len(cas.Interactions))}
	t.Cleanup(func() {
		for i, used := range r.used {
			if !used {
				t.Errorf("restamp: interaction %d (%s) was never requested; the cassette drifted beyond request bodies, re-record it with `just record`",
					i, r.cas.Interactions[i].Request.URL)
				return
			}
		}
		require.NoError(t, r.cas.Save(), "restamp: save cassette")
		t.Logf("restamp: rewrote %d interactions in %s", len(r.cas.Interactions), r.cas.File)
	})
	return r
}

func (r *restampRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil && req.Body != http.NoBody {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("restamp: read request body: %w", err)
		}
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(b))
		body = b
	}

	want := fingerprint(body)

	r.mu.Lock()
	defer r.mu.Unlock()
	for i, in := range r.cas.Interactions {
		if r.used[i] || in.Request.Method != req.Method || in.Request.URL != req.URL.String() {
			continue
		}
		if fingerprint([]byte(in.Request.Body)) != want {
			continue
		}
		r.used[i] = true
		in.Request.Body = string(body)
		in.Request.ContentLength = int64(len(body))
		return in.GetHTTPResponse()
	}
	return nil, fmt.Errorf("restamp: no unused interaction for %s %s (%s); re-record with `just record`", req.Method, req.URL, want)
}

// fingerprint identifies a chat completion request by the parts restamping
// leaves alone: the model and the conversation after the system message. That
// separates the turns of a conversation from each other and from the side
// traffic the session generates concurrently (titles, summaries), which the
// cassettes do not always record. Non-JSON bodies (fetch, sourcegraph) have no
// fingerprint and pair on method and URL alone.
func fingerprint(body []byte) string {
	var payload struct {
		Model    string            `json:"model"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	h := sha256.New()
	fmt.Fprintf(h, "model=%s\n", payload.Model)
	for _, msg := range payload.Messages[min(1, len(payload.Messages)):] {
		h.Write(msg)
	}
	return fmt.Sprintf("model %s, %d messages, %x", payload.Model, len(payload.Messages), h.Sum(nil)[:6])
}

func (r *restampRecorder) GetDefaultClient() *http.Client {
	return &http.Client{Transport: r}
}

// restampMarshal mirrors the YAML formatting charm.land/x/vcr records with, so
// restamped cassettes stay byte-comparable with recorded ones.
func restampMarshal(in any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	enc.CompactSeqIndent()
	if err := enc.Encode(in); err != nil {
		return nil, fmt.Errorf("restamp: encode yaml: %w", err)
	}
	return buf.Bytes(), nil
}
