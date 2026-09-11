//go:build !darwin

package notification

import (
	_ "embed"
)

//go:embed harness-icon-solo.png
var Icon []byte
