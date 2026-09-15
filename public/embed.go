package public

import "embed"

//go:embed admin.html admin.js vendor
var Assets embed.FS
