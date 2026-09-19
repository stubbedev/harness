package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var worktreeExcludedDirNames = map[string]struct{}{
	"node_modules":        {},
	"vendor":              {},
	"target":              {},
	"build":               {},
	"dist":                {},
	"out":                 {},
	"bin":                 {},
	"obj":                 {},
	".next":               {},
	".nuxt":               {},
	".output":             {},
	".venv":               {},
	"venv":                {},
	"__pycache__":         {},
	".pytest_cache":       {},
	".mypy_cache":         {},
	".ruff_cache":         {},
	".tox":                {},
	".gradle":             {},
	".cache":              {},
	".idea":               {},
	".vscode":             {},
	"coverage":            {},
	".turbo":              {},
	".terraform":          {},
	"Pods":                {},
	".dart_tool":          {},
	".parcel-cache":       {},
	"elm-stuff":           {},
	"_build":              {},
	"deps":                {},
	"bower_components":    {},
	"cmake-build-debug":   {},
	"cmake-build-release": {},
}

type worktreeExclusions struct {
	trackedDirs map[string]struct{}
}

func worktreeTrackedDirs(index []byte) map[string]struct{} {
	dirs := make(map[string]struct{})
	for entry := range bytes.SplitSeq(index, []byte{0}) {
		if _, file, found := bytes.Cut(entry, []byte{'\t'}); found {
			for dir := path.Dir(string(file)); dir != "."; dir = path.Dir(dir) {
				dirs[dir] = struct{}{}
			}
		}
	}
	return dirs
}

func (e *worktreeExclusions) skipDir(walkPath, name string) bool {
	if e == nil {
		return false
	}
	if _, excluded := worktreeExcludedDirNames[name]; !excluded {
		return false
	}
	_, tracked := e.trackedDirs[walkPath]
	return !tracked
}

type worktreeEntry struct {
	Mode fs.FileMode
	Hash [sha256.Size]byte
	Link string
}

func equalWorktreeEntries(a, b map[string]worktreeEntry) bool {
	return maps.Equal(a, b)
}

func snapshotWorktree(ctx context.Context, source, destination string, exclusions *worktreeExclusions) (map[string]worktreeEntry, []string, error) {
	src, err := os.OpenRoot(source)
	if err != nil {
		return nil, nil, err
	}
	defer src.Close()
	var dst *os.Root
	if destination != "" {
		if err := os.MkdirAll(destination, 0o700); err != nil {
			return nil, nil, err
		}
		dst, err = os.OpenRoot(destination)
		if err != nil {
			return nil, nil, err
		}
		defer dst.Close()
	}
	entries := make(map[string]worktreeEntry)
	var excluded []string
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
		if entry.IsDir() && exclusions.skipDir(path, entry.Name()) {
			excluded = append(excluded, path)
			return fs.SkipDir
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
		return nil, nil, err
	}
	if dst != nil {
		for path, entry := range entries {
			if entry.Mode.IsDir() {
				if err := dst.Chmod(filepath.FromSlash(path), entry.Mode.Perm()); err != nil {
					return nil, nil, err
				}
			}
		}
	}
	return entries, excluded, nil
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
