package accesslog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// SocketPath is where the controller that runs the bundled proxy offers its
// entries. A `dr release` run by `docker exec` is another process, so it reads
// them from here.
const SocketPath = "/run/docker-release/accesslog.sock"

// queueSize is how many entries a slow reader may fall behind before it is
// disconnected. A disconnected reader knows it lost entries; it never sees a gap.
const queueSize = 8192

// Serve sends every entry published to hub to each client that connects to the
// unix socket at path, one JSON object per line, until ctx ends.
func Serve(ctx context.Context, hub *Hub, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		return err
	}
	go func() {
		<-ctx.Done()
		listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go serveConn(ctx, hub, conn)
	}
}

func serveConn(ctx context.Context, hub *Hub, conn net.Conn) {
	defer conn.Close()
	entries := make(chan Entry, queueSize)
	var overflowed atomic.Bool
	cancel := hub.Subscribe(func(e Entry) {
		select {
		case entries <- e:
		default:
			overflowed.Store(true)
		}
	})
	defer cancel()

	// The hello tells the client it is subscribed, so entries after it are not missed.
	if _, err := io.WriteString(conn, "ready\n"); err != nil {
		return
	}
	gone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		close(gone)
	}()

	encoder := json.NewEncoder(conn)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case e := <-entries:
			if err := encoder.Encode(e); err != nil {
				return
			}
		case <-tick.C:
			if overflowed.Load() {
				return
			}
		case <-gone:
			return
		case <-ctx.Done():
			return
		}
	}
}

// Client reads entries from a controller's socket.
type Client struct{ Path string }

// Subscribe calls fn for each entry the controller publishes. lost closes if
// the stream ends for any reason other than cancel, and the entries after that
// point are not seen.
func (c Client) Subscribe(fn func(Entry)) (cancel func(), lost <-chan struct{}, err error) {
	conn, err := net.DialTimeout("unix", c.Path, 5*time.Second)
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to the proxy's access log: %w", err)
	}
	reader := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if hello, err := reader.ReadString('\n'); err != nil || hello != "ready\n" {
		conn.Close()
		return nil, nil, errors.New("the proxy's access log did not answer")
	}
	_ = conn.SetReadDeadline(time.Time{})

	var cancelled atomic.Bool
	lostCh := make(chan struct{})
	go func() {
		decoder := json.NewDecoder(reader)
		for {
			var e Entry
			if err := decoder.Decode(&e); err != nil {
				break
			}
			fn(e)
		}
		if !cancelled.Load() {
			close(lostCh)
		}
	}()
	return func() {
		cancelled.Store(true)
		conn.Close()
	}, lostCh, nil
}
