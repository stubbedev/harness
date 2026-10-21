package fsext

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

func RemoveAllWritable(path string) error {
	if err := MakeDirsWritable(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.RemoveAll(path)
}

func MakeDirsWritable(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if perm := info.Mode().Perm(); perm&0o700 != 0o700 {
			return os.Chmod(path, perm|0o700)
		}
		return nil
	})
}
