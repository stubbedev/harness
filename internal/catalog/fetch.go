package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// maxFetchBody caps how much of a catalog response is read into
// memory. The catalogs are a few MiB; anything larger is a broken
// mirror, not something to parse.
const maxFetchBody = 64 << 20

// FetchBytes performs a GET and returns up to maxFetchBody bytes of
// the response body. Single source of the catalog fetchers' prologue:
// create request, do, status check, capped read.
func FetchBytes(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("could not create request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s: %w", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d from %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBody))
	if err != nil {
		return nil, fmt.Errorf("failed to read response from %s: %w", url, err)
	}
	return body, nil
}

// FetchJSON is FetchBytes followed by a JSON decode into v.
func FetchJSON(ctx context.Context, client *http.Client, url string, v any) error {
	body, err := FetchBytes(ctx, client, url)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("failed to decode %s: %w", url, err)
	}
	return nil
}
