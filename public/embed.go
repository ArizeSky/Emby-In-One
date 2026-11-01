package public

import "embed"

//go:embed admin.html admin.js admin-activity.js admin-theme.js vendor web
var Assets embed.FS
