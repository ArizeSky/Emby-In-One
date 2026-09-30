package public

import "embed"

//go:embed admin.html admin.js vendor web
var Assets embed.FS
