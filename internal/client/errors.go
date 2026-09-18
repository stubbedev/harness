package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
)

// Typed outcomes callers need to distinguish from ordinary transport
// failures. Match them with errors.Is.
var (
	// ErrNotFound reports that the server answered 404. For a
	// workspace-scoped call this means the server no longer knows the
	// workspace — it was torn down, or the server was replaced under the
	// client — so the right response is to re-register rather than retry
	// the same ID, which can never start succeeding again.
	ErrNotFound = errors.New("not found")

	// ErrServerBusy reports that the server declined to shut down
	// because it is still hosting workspaces or is midway through
	// creating one. A client asking a version-mismatched server to stand
	// down must keep using it instead of assuming it is going away.
	ErrServerBusy = errors.New("server busy")

	// ErrServerShuttingDown reports that the server refused the request
	// because it has already committed to exiting. The work is not lost:
	// a replacement server can be started and the request retried
	// against it.
	ErrServerShuttingDown = errors.New("server is shutting down")

	// ErrUnsupported reports that the running server does not understand
	// the request because it predates the feature. Callers must decide
	// what is safe to do with an older server rather than treating the
	// failure as transient.
	ErrUnsupported = errors.New("unsupported by the running server")
)

// checkStatus returns nil when rsp's status code is one of ok
// (http.StatusOK when none are given). Otherwise it returns an error
// carrying the status code and, when the body decodes as a proto.Error,
// the server-provided message. Statuses that callers act on are wrapped
// in the matching sentinel. checkStatus may consume the response body.
func checkStatus(rsp *http.Response, ok ...int) error {
	if len(ok) == 0 {
		ok = []int{http.StatusOK}
	}
	if slices.Contains(ok, rsp.StatusCode) {
		return nil
	}
	var err error
	if msg := decodeErrorMessage(rsp.Body); msg != "" {
		err = fmt.Errorf("status code %d: %s", rsp.StatusCode, msg)
	} else {
		err = fmt.Errorf("status code %d", rsp.StatusCode)
	}
	switch rsp.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	case http.StatusServiceUnavailable:
		return fmt.Errorf("%w: %w", ErrServerShuttingDown, err)
	}
	return err
}

// okOrError closes rsp and returns nil when its status is OK.
// Otherwise it returns a checkStatus error prefixed with op, so callers
// get the sentinel mapping (ErrNotFound, ErrServerShuttingDown) for
// free and every method phrases the failure the same way.
func okOrError(rsp *http.Response, op string) error {
	defer rsp.Body.Close()
	if err := checkStatus(rsp); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	return nil
}

// decodeJSON closes rsp, verifies the status is OK with checkStatus,
// and decodes the body into v. op prefixes the status failure; decodeOp
// names the payload in the decode failure.
func decodeJSON(rsp *http.Response, v any, op, decodeOp string) error {
	defer rsp.Body.Close()
	if err := checkStatus(rsp); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if err := json.NewDecoder(rsp.Body).Decode(v); err != nil {
		return fmt.Errorf("failed to decode %s: %w", decodeOp, err)
	}
	return nil
}

// decodeJSONAllowEmpty behaves like decodeJSON but accepts an empty
// body on a successful status, leaving v unchanged. Endpoints that
// legitimately answer 200 with no payload need this.
func decodeJSONAllowEmpty(rsp *http.Response, v any, op, decodeOp string) error {
	defer rsp.Body.Close()
	if err := checkStatus(rsp); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if err := json.NewDecoder(rsp.Body).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("failed to decode %s: %w", decodeOp, err)
	}
	return nil
}
