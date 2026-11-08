package backend

// Read unfurled resume candidates: the App owns verified alias grouping and
// played-state reconciliation before applying the final pagination window.
func (ws *WatchStore) GetResumeCandidatePage(userID string, limit, offset int) ([]WatchProgress, error) {
	return ws.queryProgress(userID, `SELECT virtual_item_id, server_id, original_item_id, item_type,
		series_virtual_id, series_original_id, series_name, parent_index_number, index_number,
		name, production_year, provider_tmdb, position_ticks, runtime_ticks, played, is_favorite, last_played
		FROM user_watch_progress WHERE proxy_user_id = ? AND position_ticks > 0 AND played = 0
		ORDER BY last_played DESC, virtual_item_id LIMIT ? OFFSET ?`, userID, limit, offset)
}
