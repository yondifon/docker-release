package runtime

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
)

func TestUpstreamUpdatePreservesConnectionsAndRejectsBadPools(t *testing.T) {
	backend := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "%s %s", name, r.URL.Path)
		}))
	}
	old := backend("old")
	defer old.Close()
	new := backend("new")
	defer new.Close()
	file := filepath.Join(t.TempDir(), "app.upstreams")
	data, _ := json.Marshal([]string{strings.TrimPrefix(old.URL, "http://")})
	if err := os.WriteFile(file, append([]byte(fileHeader), data...), 0o644); err != nil {
		t.Fatal(err)
	}
	address := func() string {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := listener.Addr().String()
		_ = listener.Close()
		return addr
	}
	admin, public := address(), address()
	cfg := fmt.Sprintf(`{"admin":{"listen":%q},"apps":{"http":{"servers":{"test":{"listen":[%q],"routes":[{"handle":[{"handler":"reverse_proxy","dynamic_upstreams":{"source":"docker_release","file":%q}}]}]}}}}}`, admin, public, file)
	if err := caddy.Load([]byte(cfg), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = caddy.Stop() })
	conn, err := net.Dial("tcp", public)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	update := func(body string, want int) {
		t.Helper()
		url := "http://" + admin + "/docker_release/upstreams?file=" + file
		resp, err := client.Post(url, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("upstream update: status=%d body=%s", resp.StatusCode, body)
		}
	}
	data, _ = json.Marshal([]string{strings.TrimPrefix(new.URL, "http://")})
	update(string(data), http.StatusOK)
	if _, err := fmt.Fprintf(conn, "GET /kept HTTP/1.1\r\nHost: app\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(conn)
	if err != nil || !strings.Contains(string(response), "new /kept") {
		t.Fatalf("connection opened before update lost or served old backend: %s, %v", response, err)
	}
	update(`["not-an-address"]`, http.StatusBadRequest)
	resp, err := client.Get("http://" + public + "/after-rejection")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "new /after-rejection" {
		t.Fatalf("bad pool replaced the working one: status=%d body=%s", resp.StatusCode, body)
	}
}
