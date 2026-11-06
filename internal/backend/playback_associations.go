package backend

import (
	"net/http"
	"strings"
)

// Even opaque resources may not carry contradictory association hints. The
// hints never select a binding: ingress has already authenticated its capability.
func (a *App) strictBindingAssociationsMatch(r *http.Request, binding playbackIdentityBinding) bool {
	if item := r.PathValue("itemId"); item != "" && item != binding.VirtualItemID {
		return false
	}
	if user := r.PathValue("userId"); user != "" && user != binding.Owner.UserID {
		return false
	}
	for key, values := range r.URL.Query() {
		switch {
		case strings.EqualFold(key, "PlaySessionId"), strings.EqualFold(key, "EioPlaySessionId"):
			for _, value := range values {
				if _, failure := a.strictPlaybackSessionForBinding(binding, value); failure != nil {
					return false
				}
			}
		case strings.EqualFold(key, "MediaSourceId"):
			for _, value := range values {
				if value != "" {
					if _, ok := a.strictPlaybackSourceForBinding(binding, value); !ok {
						return false
					}
				}
			}
		}
	}
	return true
}
