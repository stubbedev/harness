// Command seedgen regenerates the catalog snapshot bundled in the
// binary. It fetches the live sources, runs them through the same
// translation the app uses, and writes the result gzipped so the
// checked-in file stays a fraction of the raw catalog's size.
//
// Run it with `go generate ./internal/catalog`.
package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/stubbedev/harness/internal/catalog"
)

func main() {
	out := flag.String("o", "seed.json.gz", "path to write the gzipped seed to")
	flag.Parse()

	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "seedgen:", err)
		os.Exit(1)
	}
}

func run(out string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	providers, err := catalog.FetchCatalog(ctx, nil)
	if err != nil {
		return fmt.Errorf("fetch catalog: %w", err)
	}
	if len(providers) == 0 {
		return fmt.Errorf("catalog sources returned no providers")
	}

	data, err := json.Marshal(providers)
	if err != nil {
		return fmt.Errorf("encode catalog: %w", err)
	}

	// Write through a temporary file so an interrupted run cannot leave
	// a truncated seed behind for the next build to embed.
	tmp, err := os.CreateTemp(filepath.Dir(out), ".seed-*.json.gz")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck

	gz, _ := gzip.NewWriterLevel(tmp, gzip.BestCompression)
	if _, err := gz.Write(data); err != nil {
		return fmt.Errorf("compress catalog: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("compress catalog: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}
	if err := os.Rename(tmp.Name(), out); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}

	models := 0
	for _, p := range providers {
		models += len(p.Models)
	}
	fmt.Printf("wrote %s: %d providers, %d models\n", out, len(providers), models)
	return nil
}
