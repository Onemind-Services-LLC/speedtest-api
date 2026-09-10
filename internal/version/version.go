package version

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var source string

// String identifies the application release independently of the wire protocol.
var String = strings.TrimSpace(source)
