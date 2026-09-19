package jobs

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/BeFeast/RehearseKit/internal/api/respond"
)

// HeartbeatInterval is how often an SSE comment is sent to keep proxies
// from closing an idle stream. It doubles as a catch-up poll.
var HeartbeatInterval = 15 * time.Second

// events streams job_events as Server-Sent Events:
//
//	id: <event id>
//	event: status
//	data: {"status":..,"progress":..,"message":..,"at":..}
//
// Rows after Last-Event-ID are replayed first; live rows arrive via the
// broker's LISTEN connection. The stream ends after a terminal status.
func (h *Handlers) events(w http.ResponseWriter, r *http.Request) {
	j, ok := h.LoadReadable(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		respond.Failf(w, http.StatusInternalServerError, "streaming_unsupported", "response writer cannot stream")
		return
	}
	var lastID int64
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			lastID = n
		}
	}
	if v := r.URL.Query().Get("last_event_id"); v != "" && lastID == 0 {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			lastID = n
		}
	}

	// Subscribe before the replay query so nothing falls in the gap.
	wake, unsubscribe := h.broker.Subscribe(j.ID)
	defer unsubscribe()

	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream")
	hdr.Set("Cache-Control", "no-cache")
	hdr.Set("Connection", "keep-alive")
	hdr.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	flusher.Flush()

	ctx := r.Context()
	catchUp := func() (done bool, err error) {
		evs, err := h.store.Events(ctx, j.ID, lastID)
		if err != nil {
			return false, err
		}
		for _, e := range evs {
			if err := writeEvent(w, e); err != nil {
				return true, err
			}
			lastID = e.ID
			if IsTerminal(e.Status) {
				flusher.Flush()
				return true, nil
			}
		}
		if len(evs) > 0 {
			flusher.Flush()
		}
		return false, nil
	}

	if done, err := catchUp(); done || err != nil {
		if err != nil && ctx.Err() == nil {
			slog.Warn("sse: replay", "job", j.ID, "err", err)
		}
		return
	}
	if IsTerminal(j.Status) && lastID > 0 {
		// The client already has the terminal event.
		return
	}

	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
		if done, err := catchUp(); done || err != nil {
			if err != nil && ctx.Err() == nil {
				slog.Warn("sse: catch-up", "job", j.ID, "err", err)
			}
			return
		}
	}
}

func writeEvent(w http.ResponseWriter, e Event) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: status\ndata: %s\n\n", e.ID, data)
	return err
}
