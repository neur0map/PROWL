//go:build !darwin

package notification

import (
	_ "embed"
)

//go:embed prowl-icon-solo.png
var Icon []byte
