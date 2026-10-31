package public

import "embed"

//go:embed admin.html admin.js admin-activity.js vendor web
var Assets embed.FS
