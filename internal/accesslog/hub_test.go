package accesslog

import (
	"bytes"
	"strings"
	"testing"
)

func parseStatusLine(line string) (Entry, bool) {
	status, ok := strings.CutPrefix(line, "status=")
	if !ok {
		return Entry{}, false
	}
	return Entry{Status: len(status), Upstream: status}, true
}

func TestTeeKeepsOutputAndFindsLinesSplitAcrossWrites(t *testing.T) {
	var out bytes.Buffer
	hub := &Hub{}
	var got []string
	hub.Subscribe(func(e Entry) { got = append(got, e.Upstream) })
	w := Tee(&out, parseStatusLine, hub)

	input := "noise\nstatus=a\nstatus=b-split-across"
	for _, chunk := range []string{input[:20], input[20:30], input[30:], "-writes\nstatus=c\n"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}

	if want := "noise\nstatus=a\nstatus=b-split-across-writes\nstatus=c\n"; out.String() != want {
		t.Errorf("output changed: %q", out.String())
	}
	if want := []string{"a", "b-split-across-writes", "c"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("entries = %v, want %v", got, want)
	}
}

func TestSubscriberStopsReceivingAfterCancel(t *testing.T) {
	hub := &Hub{}
	count := 0
	cancel := hub.Subscribe(func(Entry) { count++ })
	hub.Publish(Entry{})
	cancel()
	hub.Publish(Entry{})
	if count != 1 {
		t.Errorf("got %d entries, want 1", count)
	}
}
