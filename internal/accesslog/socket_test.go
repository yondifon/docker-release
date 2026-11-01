package accesslog

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func serveForTest(t *testing.T) (*Hub, string, context.CancelFunc) {
	t.Helper()
	// A unix socket path is limited to about 100 bytes, so avoid long temp paths.
	dir, err := os.MkdirTemp("/tmp", "al")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "log.sock")
	hub := &Hub{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go Serve(ctx, hub, path)
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return hub, path, cancel
}

func TestAnotherProcessSeesEveryEntryPublishedAfterItSubscribed(t *testing.T) {
	hub, path, _ := serveForTest(t)
	got := make(chan Entry, 100)
	cancel, _, err := Client{Path: path}.Subscribe(func(e Entry) { got <- e })
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	for i := 1; i <= 50; i++ {
		hub.Publish(Entry{Status: 500, Upstream: "172.20.0.4:80", Host: "app.localhost", Path: "/signup"})
		hub.Publish(Entry{Status: 200 + i})
	}

	for i := 1; i <= 50; i++ {
		for _, want := range []int{500, 200 + i} {
			select {
			case e := <-got:
				if e.Status != want {
					t.Fatalf("entry %d: status %d, want %d", i, e.Status, want)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("entry %d never arrived", i)
			}
		}
	}
}

func TestClientKnowsWhenTheStreamBreaks(t *testing.T) {
	_, path, stop := serveForTest(t)
	cancel, lost, err := Client{Path: path}.Subscribe(func(Entry) {})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	select {
	case <-lost:
		t.Fatal("lost closed while the server was running")
	case <-time.After(200 * time.Millisecond):
	}
	stop()
	select {
	case <-lost:
	case <-time.After(2 * time.Second):
		t.Fatal("lost did not close after the server stopped")
	}
}

func TestCancelIsNotReportedAsLost(t *testing.T) {
	_, path, _ := serveForTest(t)
	cancel, lost, err := Client{Path: path}.Subscribe(func(Entry) {})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-lost:
		t.Fatal("a cancelled subscription must not look like a broken stream")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestSubscribeFailsWhenNothingServes(t *testing.T) {
	if _, _, err := (Client{Path: "/tmp/no-such-accesslog.sock"}).Subscribe(func(Entry) {}); err == nil {
		t.Fatal("want an error when no controller serves the log")
	}
}
