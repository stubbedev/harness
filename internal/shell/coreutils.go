package shell

import (
	"os"
	"runtime"
	"strconv"

	"github.com/stubbedev/harness/internal/envvars"
)

var useGoCoreUtils bool

func init() {
	// If HARNESS_CORE_UTILS is set to either true or false, respect that.
	// By default, enable on Windows only.
	if v, err := strconv.ParseBool(os.Getenv(envvars.CoreUtils)); err == nil {
		useGoCoreUtils = v
	} else {
		useGoCoreUtils = runtime.GOOS == "windows"
	}
}
