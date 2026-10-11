package backend

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

const (
	maxHLSSegmentRewriteSize = 64 << 20

	// maxHLSManifestSize caps the manifest body buffered for rewriting; real
	// playlists are kilobytes of text, so 8 MiB only rejects hostile or
	// broken upstreams.
	maxHLSManifestSize = 8 << 20
)

// Buffer only eligible VOD segments. Oversized segments keep streaming, and a
// failed read must not be sent as a successful truncated 200 response.
func rewriteHLSSegmentBody(r *http.Request, resp *http.Response, session, rest string) ([]byte, bool, error) {
	if r.Method != http.MethodGet || resp.StatusCode != http.StatusOK || r.Header.Get("Range") != "" || resp.Header.Get("Content-Encoding") != "" {
		return nil, false, nil
	}
	lower := strings.ToLower(rest)
	if !strings.HasSuffix(lower, ".mp4") && !strings.HasSuffix(lower, ".m4s") {
		return nil, false, nil
	}
	position, ok := hlsSegmentStartPosition(session, rest)
	if !ok || resp.ContentLength > maxHLSSegmentRewriteSize {
		return nil, false, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHLSSegmentRewriteSize+1))
	if err != nil {
		return nil, false, err
	}
	if len(body) > maxHLSSegmentRewriteSize {
		resp.Body = &prefixedSegmentBody{Reader: io.MultiReader(bytes.NewReader(body), resp.Body), original: resp.Body}
		return nil, false, nil
	}
	if resp.ContentLength >= 0 && int64(len(body)) != resp.ContentLength {
		return nil, false, io.ErrUnexpectedEOF
	}
	if rewriteFMP4Timeline(body, position) {
		// Upstream validators describe different bytes after timeline rewriting.
		resp.Header.Del("ETag")
		resp.Header.Del("Last-Modified")
	}
	return body, true, nil
}

type prefixedSegmentBody struct {
	io.Reader
	original io.Closer
}

func (b *prefixedSegmentBody) Close() error { return b.original.Close() }
