// Package accesslog reads the access log of the proxy bundled into the
// controller's container, so a release can count the errors users get from
// each app container.
package accesslog

import (
	"bytes"
	"io"
	"sync"
)

// Entry is one request the proxy answered.
type Entry struct {
	Status   int    `json:"status"`
	Upstream string `json:"upstream"` // `ip:port` of the app container that answered
	Host     string `json:"host"`
	Path     string `json:"path"` // URL path without the query string
}

// Parser turns one log line into an Entry. It reports false for lines that are
// not requests sent to an app container.
type Parser func(line string) (Entry, bool)

// Hub hands each parsed entry to every subscriber.
type Hub struct {
	mu   sync.Mutex
	next int
	subs map[int]func(Entry)
}

// Default is the hub the bundled proxy writes to and releases read from.
var Default = &Hub{}

// Subscribe calls fn for every entry published until the returned function is
// called. fn runs on the proxy's output path, so it must return quickly.
func (h *Hub) Subscribe(fn func(Entry)) (cancel func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs == nil {
		h.subs = make(map[int]func(Entry))
	}
	id := h.next
	h.next++
	h.subs[id] = fn
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.subs, id)
	}
}

// Publish sends e to every subscriber.
func (h *Hub) Publish(e Entry) {
	h.mu.Lock()
	subs := make([]func(Entry), 0, len(h.subs))
	for _, fn := range h.subs {
		subs = append(subs, fn)
	}
	h.mu.Unlock()
	for _, fn := range subs {
		fn(e)
	}
}

// maxLine caps how much of a line without a newline is kept, so a broken
// writer can't grow the buffer without end.
const maxLine = 64 << 10

// Tee returns a writer that passes every byte to out unchanged and publishes
// each complete line that parse accepts to hub. A write to out that fails is
// reported to the caller; parsing never fails a write.
func Tee(out io.Writer, parse Parser, hub *Hub) io.Writer {
	return &tee{out: out, parse: parse, hub: hub}
}

type tee struct {
	out   io.Writer
	parse Parser
	hub   *Hub

	mu      sync.Mutex
	partial []byte
}

func (t *tee) Write(p []byte) (int, error) {
	n, err := t.out.Write(p)

	t.mu.Lock()
	defer t.mu.Unlock()
	data := append(t.partial, p[:n]...)
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		if entry, ok := t.parse(string(data[:i])); ok {
			t.hub.Publish(entry)
		}
		data = data[i+1:]
	}
	if len(data) > maxLine {
		data = nil
	}
	t.partial = append(t.partial[:0], data...)
	return n, err
}
