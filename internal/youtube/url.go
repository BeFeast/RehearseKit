// Package youtube implements POST /api/v1/youtube/preview: it validates a
// YouTube link, extracts the video id and asks yt-dlp for the metadata the
// SPA shows before the user commits to a job. Results are cached in memory
// and the endpoint is rate limited per client IP because every miss spawns
// a yt-dlp process.
package youtube

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// ErrInvalidURL is returned for anything that is not a recognisable link to
// a single YouTube video.
var ErrInvalidURL = errors.New("not a YouTube video URL")

var videoIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// ParseVideoID extracts the 11-character video id from the accepted URL
// shapes:
//
//	https://www.youtube.com/watch?v=ID      (also youtube.com, m., music.)
//	https://www.youtube.com/shorts/ID
//	https://www.youtube.com/embed/ID, /live/ID, /v/ID
//	https://youtu.be/ID
//
// A missing scheme is tolerated ("youtu.be/ID"); anything other than
// http/https is rejected.
func ParseVideoID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrInvalidURL
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", ErrInvalidURL
	}
	host := strings.ToLower(u.Hostname())
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	var id string
	switch host {
	case "youtu.be":
		id = segs[0]
	case "youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com":
		switch {
		case len(segs) == 1 && segs[0] == "watch":
			id = u.Query().Get("v")
		case len(segs) >= 2:
			switch segs[0] {
			case "shorts", "embed", "live", "v":
				id = segs[1]
			}
		}
	default:
		return "", ErrInvalidURL
	}
	if !videoIDRe.MatchString(id) {
		return "", ErrInvalidURL
	}
	return id, nil
}

// CanonicalURL is the watch URL yt-dlp is invoked with. Passing the rebuilt
// URL instead of the caller's string strips playlist/tracking parameters and
// keeps user input out of the argv entirely.
func CanonicalURL(videoID string) string {
	return "https://www.youtube.com/watch?v=" + videoID
}
