package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
)

type worktreeEntry struct {
	Mode fs.FileMode
	Hash [sha256.Size]byte
	Link string
}

func equalWorktreeEntries(a, b map[string]worktreeEntry) bool {
	return maps.Equal(a, b)
}

func snapshotWorktree(ctx context.Context, source, destination string) (map[string]worktreeEntry, error) {
	src, err := os.OpenRoot(source)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	var dst *os.Root
	if destination != "" {
		if err := os.MkdirAll(destination, 0o700); err != nil {
			return nil, err
		}
		dst, err = os.OpenRoot(destination)
		if err != nil {
			return nil, err
		}
		defer dst.Close()
	}
	entries := make(map[string]worktreeEntry)
	err = fs.WalkDir(src.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		if strings.EqualFold(entry.Name(), ".git") {
			if path != ".git" {
				return fmt.Errorf("nested Git metadata at %s; cannot safely snapshot nested repositories or submodules", path)
			}
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := src.Lstat(path)
		if err != nil {
			return err
		}
		mode := info.Mode()
		record := worktreeEntry{Mode: mode.Type() | mode.Perm()}
		switch {
		case mode.IsDir():
			if dst != nil {
				if err := dst.Mkdir(path, 0o700); err != nil {
					return err
				}
			}
		case mode&os.ModeSymlink != 0:
			record.Link, err = src.Readlink(path)
			if err != nil {
				return err
			}
			if dst != nil {
				if err := dst.Symlink(record.Link, path); err != nil {
					return err
				}
			}
		case mode.IsRegular():
			file, err := src.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			opened, err := file.Stat()
			if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
				return errors.Join(err, fmt.Errorf("file changed during snapshot: %s", path))
			}
			hash := sha256.New()
			var writer io.Writer = hash
			var target *os.File
			if dst != nil {
				target, err = dst.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
				if err != nil {
					return err
				}
				defer target.Close()
				writer = io.MultiWriter(hash, target)
			}
			if _, err := io.Copy(writer, &worktreeContextReader{ctx: ctx, reader: file}); err != nil {
				return err
			}
			if target != nil {
				if err := target.Chmod(mode.Perm()); err != nil {
					return err
				}
				if err := target.Close(); err != nil {
					return err
				}
			}
			copy(record.Hash[:], hash.Sum(nil))
		default:
			return fmt.Errorf("cannot safely snapshot special file %s (%s)", path, mode)
		}
		entries[path] = record
		return nil
	})
	if err != nil {
		return nil, err
	}
	if dst != nil {
		for path, entry := range entries {
			if entry.Mode.IsDir() {
				if err := dst.Chmod(filepath.FromSlash(path), entry.Mode.Perm()); err != nil {
					return nil, err
				}
			}
		}
	}
	return entries, nil
}

type worktreeContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *worktreeContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
