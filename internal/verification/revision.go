package verification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func (r *Runner) fingerprint(ctx context.Context, rules []Rule, paths []string) (string, error) {
	h := sha256.New()
	enc := json.NewEncoder(h)
	if err := enc.Encode(struct {
		Root   string
		Config VerificationConfig
		Paths  []string
	}{r.root, r.config, paths}); err != nil {
		return "", err
	}
	if len(rules) == 0 {
		return hex.EncodeToString(h.Sum(nil)), ctx.Err()
	}
	var globs []string
	for _, rule := range rules {
		globs = append(globs, rule.Paths...)
		globs = append(globs, rule.Inputs...)
	}
	err := filepath.WalkDir(r.root, func(p string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if p == r.root {
			return nil
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		relative, err := filepath.Rel(r.root, p)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Stat(p)
			if err == nil && target.IsDir() {
				return fmt.Errorf("cannot fingerprint directory symlink %q", relative)
			}
		}
		if !matches(globs, relative) {
			return nil
		}
		before, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !before.Mode().IsRegular() {
			return fmt.Errorf("cannot fingerprint non-regular input %q", relative)
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		content := sha256.New()
		buffer := make([]byte, 64*1024)
		for {
			if err = ctx.Err(); err != nil {
				break
			}
			var n int
			n, err = f.Read(buffer)
			if n > 0 {
				_, _ = content.Write(buffer[:n])
			}
			if err == io.EOF {
				err = nil
				break
			}
			if err != nil {
				break
			}
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		after, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() || before.Mode() != after.Mode() {
			return fmt.Errorf("input changed while fingerprinting %q", relative)
		}
		return enc.Encode(struct {
			Path string
			Mode uint32
			Hash string
		}{relative, uint32(before.Mode()), hex.EncodeToString(content.Sum(nil))})
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
