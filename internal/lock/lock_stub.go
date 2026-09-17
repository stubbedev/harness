//go:build (!unix && !windows) || aix

package lock

import (
	"context"
	"errors"
	"os"
)

// errUnsupported is returned on platforms without an advisory file-lock
// primitive in x/sys (aix; plan9 and the wasm targets never had one).
var errUnsupported = errors.New("file locks are not supported on this platform")

func lockFile(context.Context, *os.File) (func(), error) {
	return nil, errUnsupported
}

func tryLockFile(*os.File) (func(), error) {
	return nil, errUnsupported
}
