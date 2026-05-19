package webui

import _ "embed"

// IndexHTML is the embedded landing page served by the Go binary.
//
//go:embed index.html
var IndexHTML []byte
