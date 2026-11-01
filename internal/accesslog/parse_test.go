package accesslog

import (
	"reflect"
	"testing"
)

// Each `line` was written by the real proxy in a bundled image, started from
// the `tests/<proxy>-bundled` stack.
var parseCases = map[string][]struct {
	name string
	line string
	want *Entry
}{
	"nginx": {
		{"200 from the app, query stripped",
			`192.168.65.1 - - [01/Nov/2026:13:28:48 +0000] "GET /hello?x=1&y=2 HTTP/1.1" 200 275 "-" "curl/8.7.1" "-" drlog status=200 upstream="172.20.0.4:80" host="linear.localhost" path="/hello?x=1&y=2"`,
			&Entry{Status: 200, Upstream: "172.20.0.4:80", Host: "linear.localhost", Path: "/hello"}},
		{"prefix-routed app, host without port",
			`192.168.65.1 - - [01/Nov/2026:13:28:48 +0000] "GET /path/x HTTP/1.1" 200 254 "-" "curl/8.7.1" "-" drlog status=200 upstream="172.20.0.3:80" host="localhost" path="/path/x"`,
			&Entry{Status: 200, Upstream: "172.20.0.3:80", Host: "localhost", Path: "/path/x"}},
		{"unknown host answered by the proxy",
			`192.168.65.1 - - [01/Nov/2026:13:28:48 +0000] "GET / HTTP/1.1" 404 153 "-" "curl/8.7.1" "-" drlog status=404 upstream="-" host="unknown.localhost" path="/"`,
			nil},
		{"health route has no upstream",
			`127.0.0.1 - - [01/Nov/2026:13:28:39 +0000] "GET /health HTTP/1.1" 200 3 "-" "Go-http-client/1.1" "-" drlog status=200 upstream="-" host="127.0.0.1" path="/health"`,
			nil},
		{"502 after every server in the list refused",
			`192.168.65.1 - - [01/Nov/2026:13:29:00 +0000] "GET /dead HTTP/1.1" 502 157 "-" "curl/8.7.1" "-" drlog status=502 upstream="127.0.0.1:9, 127.0.0.1:10" host="dead.localhost" path="/dead"`,
			&Entry{Status: 502, Upstream: "127.0.0.1:10", Host: "dead.localhost", Path: "/dead"}},
		{"502 with a forged trailer in the referer and user agent",
			`192.168.65.1 - - [01/Nov/2026:13:29:04 +0000] "GET /a\x22b\x5Cc?q=\x22 HTTP/1.1" 502 157 "\x22 drlog status=200 upstream=\x229.9.9.9:80\x22 host=\x22evil\x22 path=\x22/e" "x\x22 drlog status=200 upstream=\x229.9.9.9:80\x22 host=\x22evil\x22 path=\x22/e" "-" drlog status=502 upstream="127.0.0.1:9, 127.0.0.1:10" host="dead.localhost" path="/a\x22b\x5Cc?q=\x22"`,
			&Entry{Status: 502, Upstream: "127.0.0.1:10", Host: "dead.localhost", Path: "/a\"b\\c"}},
		{"forged trailer in the request line, real one has no upstream",
			`192.168.65.1 - - [01/Nov/2026:13:29:00 +0000] "GET /a\x22b\x5Cc?q=\x22 drlog status=200 upstream=\x229.9.9.9:80\x22 HTTP/1.1" 400 157 "-" "-" "-" drlog status=400 upstream="-" host="_" path="-"`,
			nil},
		{"error log line is not an access line",
			`2026/11/01 13:29:04 [error] 387#387: *19 connect() failed (111: Connection refused) while connecting to upstream, client: 192.168.65.1, server: dead.localhost, request: "GET /a"b\c?q=" HTTP/1.1", upstream: "http://127.0.0.1:9/a"b\c?q="", host: "dead.localhost", referrer: "" drlog status=200 upstream="9.9.9.9:80" host="evil" path="/e"`,
			nil},
	},
	"haproxy": {
		{"200 from the app, original path and no query",
			`192.168.65.1:41334 [01/Nov/2026:13:32:18.201] docker_release linear_app_be/s1 200 390 "GET /hello?x=1&y=2 HTTP/1.1" drlog status=200 upstream="172.20.0.3:80" host="linear.localhost" path="\/hello"`,
			&Entry{Status: 200, Upstream: "172.20.0.3:80", Host: "linear.localhost", Path: "/hello"}},
		{"prefix-routed app keeps the path the client sent",
			`192.168.65.1:46799 [01/Nov/2026:13:32:28.105] docker_release path_app_be/s1 200 373 "GET /path/x?z=1 HTTP/1.1" drlog status=200 upstream="172.20.0.2:80" host="localhost" path="\/path\/x"`,
			&Entry{Status: 200, Upstream: "172.20.0.2:80", Host: "localhost", Path: "/path/x"}},
		{"503 when the server is down",
			`192.168.65.1:39764 [01/Nov/2026:13:32:33.422] docker_release linear_app_be/s1 503 217 "GET /k1 HTTP/1.1" drlog status=503 upstream="172.20.0.3:80" host="linear.localhost" path="\/k1"`,
			&Entry{Status: 503, Upstream: "172.20.0.3:80", Host: "linear.localhost", Path: "/k1"}},
		{"quote and backslash in the path",
			`192.168.65.1:37654 [01/Nov/2026:13:32:28.077] docker_release linear_app_be/s1 200 385 "GET /a#22b\c?q=#22 HTTP/1.1" drlog status=200 upstream="172.20.0.3:80" host="linear.localhost" path="\/a\"b\\c"`,
			&Entry{Status: 200, Upstream: "172.20.0.3:80", Host: "linear.localhost", Path: "/a\"b\\c"}},
		{"unknown host answered by the proxy",
			`192.168.65.1:35031 [01/Nov/2026:13:32:18.243] docker_release docker_release/<NOSRV> 404 56 "GET / HTTP/1.1" drlog status=404 upstream="-:-" host="unknown.localhost" path="\/"`,
			nil},
		{"bad request",
			`192.168.65.1:38011 [01/Nov/2026:13:32:18.747] docker_release docker_release/<NOSRV> 400 0 "<BADREQ>" drlog status=400 upstream="-:-" host="-" path="-"`,
			nil},
	},
	"traefik": {
		{"200 from the app, query stripped",
			`{"ClientAddr":"192.168.65.1:56725","ClientHost":"192.168.65.1","ClientPort":"56725","ClientUsername":"-","DownstreamContentSize":390,"DownstreamStatus":200,"Duration":3960625,"OriginContentSize":390,"OriginDuration":3582875,"OriginStatus":200,"Overhead":377750,"RequestAddr":"linear.localhost","RequestContentSize":0,"RequestCount":2,"RequestHost":"linear.localhost","RequestMethod":"GET","RequestPath":"/hello?x=1\u0026y=2","RequestPort":"-","RequestProtocol":"HTTP/1.1","RequestScheme":"http","RetryAttempts":0,"RouterName":"dr-linear_app-24c6d055234a5f7d24ffd2f7-http@file","ServiceAddr":"172.20.0.6:80","ServiceName":"dr-linear_app-24c6d055234a5f7d24ffd2f7@file","ServiceURL":"http://172.20.0.6:80","StartLocal":"2026-11-01T13:33:26.01377275Z","StartUTC":"2026-11-01T13:33:26.01377275Z","entryPointName":"web","level":"info","msg":"","time":"2026-11-01T13:33:26Z"}`,
			&Entry{Status: 200, Upstream: "172.20.0.6:80", Host: "linear.localhost", Path: "/hello"}},
		{"500 from the app",
			`{"ClientAddr":"192.168.65.1:40644","ClientHost":"192.168.65.1","ClientPort":"40644","ClientUsername":"-","DownstreamContentSize":0,"DownstreamStatus":500,"Duration":2622459,"OriginContentSize":0,"OriginDuration":2606291,"OriginStatus":500,"Overhead":16168,"RequestAddr":"linear.localhost","RequestContentSize":0,"RequestCount":6,"RequestHost":"linear.localhost","RequestMethod":"GET","RequestPath":"/health","RequestPort":"-","RequestProtocol":"HTTP/1.1","RequestScheme":"http","RetryAttempts":0,"RouterName":"dr-linear_app-24c6d055234a5f7d24ffd2f7-http@file","ServiceAddr":"172.20.0.6:80","ServiceName":"dr-linear_app-24c6d055234a5f7d24ffd2f7@file","ServiceURL":"http://172.20.0.6:80","StartLocal":"2026-11-01T13:33:26.149135625Z","StartUTC":"2026-11-01T13:33:26.149135625Z","entryPointName":"web","level":"info","msg":"","time":"2026-11-01T13:33:26Z"}`,
			&Entry{Status: 500, Upstream: "172.20.0.6:80", Host: "linear.localhost", Path: "/health"}},
		{"504 when the app does not answer",
			`{"ClientAddr":"192.168.65.1:54827","ClientHost":"192.168.65.1","ClientPort":"54827","ClientUsername":"-","DownstreamContentSize":15,"DownstreamStatus":504,"Duration":30009098334,"OriginContentSize":15,"OriginDuration":30008840417,"OriginStatus":504,"Overhead":257917,"RequestAddr":"linear.localhost","RequestContentSize":0,"RequestCount":8,"RequestHost":"linear.localhost","RequestMethod":"GET","RequestPath":"/k1","RequestPort":"-","RequestProtocol":"HTTP/1.1","RequestScheme":"http","RetryAttempts":0,"RouterName":"dr-linear_app-24c6d055234a5f7d24ffd2f7-http@file","ServiceAddr":"172.20.0.6:80","ServiceName":"dr-linear_app-24c6d055234a5f7d24ffd2f7@file","ServiceURL":"http://172.20.0.6:80","StartLocal":"2026-11-01T13:33:28.697344793Z","StartUTC":"2026-11-01T13:33:28.697344793Z","entryPointName":"web","level":"info","msg":"","time":"2026-11-01T13:33:58Z"}`,
			&Entry{Status: 504, Upstream: "172.20.0.6:80", Host: "linear.localhost", Path: "/k1"}},
		{"quote and backslash in the path",
			`{"ClientAddr":"192.168.65.1:21072","ClientHost":"192.168.65.1","ClientPort":"21072","ClientUsername":"-","DownstreamContentSize":375,"DownstreamStatus":200,"Duration":8044209,"OriginContentSize":375,"OriginDuration":8010750,"OriginStatus":200,"Overhead":33459,"RequestAddr":"linear.localhost","RequestContentSize":0,"RequestCount":7,"RequestHost":"linear.localhost","RequestMethod":"GET","RequestPath":"/a%22b%5Cc?q=\"","RequestPort":"-","RequestProtocol":"HTTP/1.1","RequestScheme":"http","RetryAttempts":0,"RouterName":"dr-linear_app-24c6d055234a5f7d24ffd2f7-http@file","ServiceAddr":"172.20.0.6:80","ServiceName":"dr-linear_app-24c6d055234a5f7d24ffd2f7@file","ServiceURL":"http://172.20.0.6:80","StartLocal":"2026-11-01T13:33:26.553223792Z","StartUTC":"2026-11-01T13:33:26.553223792Z","entryPointName":"web","level":"info","msg":"","time":"2026-11-01T13:33:26Z"}`,
			&Entry{Status: 200, Upstream: "172.20.0.6:80", Host: "linear.localhost", Path: "/a%22b%5Cc"}},
		{"unknown host answered by the proxy",
			`{"ClientAddr":"192.168.65.1:61702","ClientHost":"192.168.65.1","ClientPort":"61702","ClientUsername":"-","DownstreamContentSize":19,"DownstreamStatus":404,"Duration":65125,"GzipRatio":0,"OriginContentSize":0,"OriginDuration":0,"OriginStatus":0,"Overhead":65125,"RequestAddr":"unknown.localhost","RequestContentSize":0,"RequestCount":3,"RequestHost":"unknown.localhost","RequestMethod":"GET","RequestPath":"/","RequestPort":"-","RequestProtocol":"HTTP/1.1","RequestScheme":"http","RetryAttempts":0,"StartLocal":"2026-11-01T13:33:26.040842917Z","StartUTC":"2026-11-01T13:33:26.040842917Z","entryPointName":"web","level":"info","msg":"","time":"2026-11-01T13:33:26Z"}`,
			nil},
		{"health route has no service address",
			`{"ClientAddr":"127.0.0.1:42214","ClientHost":"127.0.0.1","ClientPort":"42214","ClientUsername":"-","DownstreamContentSize":19,"DownstreamStatus":404,"Duration":15959,"GzipRatio":0,"OriginContentSize":0,"OriginDuration":0,"OriginStatus":0,"Overhead":15959,"RequestAddr":"127.0.0.1","RequestContentSize":0,"RequestCount":1,"RequestHost":"127.0.0.1","RequestMethod":"GET","RequestPath":"/health","RequestPort":"-","RequestProtocol":"HTTP/1.1","RequestScheme":"http","RetryAttempts":0,"StartLocal":"2026-11-01T13:33:16.358000843Z","StartUTC":"2026-11-01T13:33:16.358000843Z","entryPointName":"web","level":"info","msg":"","time":"2026-11-01T13:33:16Z"}`,
			nil},
	},
	"caddy": {
		{"200 from the app, query stripped",
			`{"level":"info","ts":1793540092.339343,"logger":"http.log.access.log2","msg":"handled request","request":{"remote_ip":"192.168.65.1","remote_port":"52224","client_ip":"192.168.65.1","proto":"HTTP/1.1","method":"GET","host":"linear.localhost","uri":"/hello?x=1&y=2","headers":{"User-Agent":["curl/8.7.1"],"Accept":["*/*"]}},"bytes_read":0,"user_id":"","duration":0.005544875,"size":325,"status":200,"resp_headers":{"Via":["1.1 Caddy"],"Date":["Sun, 01 Nov 2026 13:34:52 GMT"],"Content-Length":["325"],"Content-Type":["text/plain; charset=utf-8"]},"upstream":"172.20.0.3:80"}`,
			&Entry{Status: 200, Upstream: "172.20.0.3:80", Host: "linear.localhost", Path: "/hello"}},
		{"prefix-routed app keeps the path the client sent",
			`{"level":"info","ts":1793540092.3788328,"logger":"http.log.access.log3","msg":"handled request","request":{"remote_ip":"192.168.65.1","remote_port":"20508","client_ip":"192.168.65.1","proto":"HTTP/1.1","method":"GET","host":"localhost","uri":"/path/x?z=1","headers":{"User-Agent":["curl/8.7.1"],"Accept":["*/*"]}},"bytes_read":0,"user_id":"","duration":0.002012625,"size":301,"status":200,"resp_headers":{"Via":["1.1 Caddy"],"Content-Length":["301"],"Content-Type":["text/plain; charset=utf-8"],"Date":["Sun, 01 Nov 2026 13:34:52 GMT"]},"upstream":"172.20.0.6:80"}`,
			&Entry{Status: 200, Upstream: "172.20.0.6:80", Host: "localhost", Path: "/path/x"}},
		{"500 from the app",
			`{"level":"error","ts":1793540092.4036763,"logger":"http.log.access.log2","msg":"handled request","request":{"remote_ip":"192.168.65.1","remote_port":"38729","client_ip":"192.168.65.1","proto":"HTTP/1.1","method":"GET","host":"linear.localhost","uri":"/health","headers":{"Accept":["*/*"],"User-Agent":["curl/8.7.1"]}},"bytes_read":0,"user_id":"","duration":0.000426625,"size":0,"status":500,"resp_headers":{"Via":["1.1 Caddy"],"Date":["Sun, 01 Nov 2026 13:34:52 GMT"],"Content-Length":["0"]},"upstream":"172.20.0.3:80"}`,
			&Entry{Status: 500, Upstream: "172.20.0.3:80", Host: "linear.localhost", Path: "/health"}},
		{"502 when the app is down",
			`{"level":"error","ts":1793540096.1389184,"logger":"http.log.access.log2","msg":"handled request","request":{"remote_ip":"192.168.65.1","remote_port":"45059","client_ip":"192.168.65.1","proto":"HTTP/1.1","method":"GET","host":"linear.localhost","uri":"/k1","headers":{"User-Agent":["curl/8.7.1"],"Accept":["*/*"]}},"bytes_read":0,"user_id":"","duration":3.001364292,"size":0,"status":502,"resp_headers":{"Server":["Caddy"]},"upstream":"172.20.0.3:80"}`,
			&Entry{Status: 502, Upstream: "172.20.0.3:80", Host: "linear.localhost", Path: "/k1"}},
		{"quote and backslash in the path and a header",
			`{"level":"info","ts":1793540092.8029916,"logger":"http.log.access.log2","msg":"handled request","request":{"remote_ip":"192.168.65.1","remote_port":"55649","client_ip":"192.168.65.1","proto":"HTTP/1.1","method":"GET","host":"linear.localhost","uri":"/a\"b\\c?q=\"","headers":{"User-Agent":["x\" drlog"],"Connection":["close"]}},"bytes_read":0,"user_id":"","duration":0.002935583,"size":310,"status":200,"resp_headers":{"Via":["1.1 Caddy"],"Date":["Sun, 01 Nov 2026 13:34:52 GMT"],"Content-Length":["310"],"Content-Type":["text/plain; charset=utf-8"]},"upstream":"172.20.0.3:80"}`,
			&Entry{Status: 200, Upstream: "172.20.0.3:80", Host: "linear.localhost", Path: "/a\"b\\c"}},
		{"unknown host answered by the proxy",
			`{"level":"info","ts":1793540092.3520982,"logger":"http.log.access.log3","msg":"handled request","request":{"remote_ip":"192.168.65.1","remote_port":"62204","client_ip":"192.168.65.1","proto":"HTTP/1.1","method":"GET","host":"unknown.localhost","uri":"/","headers":{"User-Agent":["curl/8.7.1"],"Accept":["*/*"]}},"bytes_read":0,"user_id":"","duration":0.000103084,"size":0,"status":404,"resp_headers":{"Server":["Caddy"],"Content-Type":[]}}`,
			nil},
		{"health route has no upstream",
			`{"level":"info","ts":1793540092.3643122,"logger":"http.log.access.log3","msg":"handled request","request":{"remote_ip":"192.168.65.1","remote_port":"60318","client_ip":"192.168.65.1","proto":"HTTP/1.1","method":"GET","host":"localhost:8091","uri":"/health","headers":{"User-Agent":["curl/8.7.1"],"Accept":["*/*"]}},"bytes_read":0,"user_id":"","duration":0.0001095,"size":2,"status":200,"resp_headers":{"Content-Type":["text/plain; charset=utf-8"],"Server":["Caddy"]}}`,
			nil},
	},
}

// Lines built by hand: the real proxies refuse a request that could produce
// them, so the escaping rules of each format are applied to the forged text.
var forgedCases = []struct {
	name  string
	proxy string
	line  string
	want  *Entry
}{
	{"haproxy: host holds an escaped copy of the trailer", "haproxy",
		`192.168.65.1:1 [01/Nov/2026:13:32:18.201] docker_release docker_release/<NOSRV> 404 56 "GET / HTTP/1.1" drlog status=404 upstream="-:-" host="x drlog status=200 upstream=\"9.9.9.9:80\" host=\"e\" path=\"\/e" path="\/"`,
		nil},
	{"haproxy: path holds an escaped copy of the trailer", "haproxy",
		`192.168.65.1:1 [01/Nov/2026:13:32:18.201] docker_release linear_app_be/s1 502 56 "GET /x HTTP/1.1" drlog status=502 upstream="172.20.0.3:80" host="linear.localhost" path="\/x drlog status=200 upstream=\"9.9.9.9:80\" host=\"e\" path=\"\/e"`,
		&Entry{Status: 502, Upstream: "172.20.0.3:80", Host: "linear.localhost", Path: `/x drlog status=200 upstream="9.9.9.9:80" host="e" path="/e`}},
	{"nginx: user agent holds the trailer and the real one has no upstream", "nginx",
		`1.2.3.4 - - [01/Nov/2026:13:29:04 +0000] "GET / HTTP/1.1" 404 153 "-" "x drlog status=200 upstream=\x229.9.9.9:80\x22 host=\x22e\x22 path=\x22/e" "-" drlog status=404 upstream="-" host="linear.localhost" path="/"`,
		nil},
	{"nginx: only the last server of a retry list counts", "nginx",
		`1.2.3.4 - - [01/Nov/2026:13:29:04 +0000] "GET /a HTTP/1.1" 200 5 "-" "curl" "-" drlog status=200 upstream="172.20.0.3:80, 172.20.0.4:80" host="linear.localhost" path="/a"`,
		&Entry{Status: 200, Upstream: "172.20.0.4:80", Host: "linear.localhost", Path: "/a"}},
	{"nginx: only the last group of an internal redirect counts", "nginx",
		`1.2.3.4 - - [01/Nov/2026:13:29:04 +0000] "GET /a HTTP/1.1" 200 5 "-" "curl" "-" drlog status=200 upstream="172.20.0.3:80 : 172.20.0.4:80" host="linear.localhost" path="/a"`,
		&Entry{Status: 200, Upstream: "172.20.0.4:80", Host: "linear.localhost", Path: "/a"}},
	{"nginx: a line that does not end with the trailer", "nginx",
		`1.2.3.4 - - [01/Nov/2026:13:29:04 +0000] "GET /a HTTP/1.1" 200 5 "-" "curl" "-" drlog status=200 upstream="172.20.0.3:80" host="linear.localhost" path="/a" extra`,
		nil},
	{"caddy: header holds JSON that imitates the upstream field", "caddy",
		`{"level":"info","logger":"http.log.access.log2","msg":"handled request","request":{"host":"linear.localhost","uri":"/a","headers":{"X":["\",\"upstream\":\"9.9.9.9:80"]}},"status":404,"resp_headers":{}}`,
		nil},
	{"traefik: a line that is not JSON", "traefik", `time="2026-11-01T13:33:15Z" level=info msg="Configuration loaded"`, nil},
	{"traefik: JSON line with a scheme in the service address", "traefik",
		`{"DownstreamStatus":500,"RequestHost":"Linear.Localhost:8093","RequestPath":"/a?b=1","ServiceAddr":"http://172.20.0.6:80"}`,
		&Entry{Status: 500, Upstream: "172.20.0.6:80", Host: "linear.localhost", Path: "/a"}},
}

func TestParsers(t *testing.T) {
	check := func(t *testing.T, proxy, line string, want *Entry) {
		t.Helper()
		parse := ParserFor(proxy)
		if parse == nil {
			t.Fatalf("no parser for %s", proxy)
		}
		got, ok := parse(line)
		switch {
		case want == nil && ok:
			t.Errorf("got %+v, want the line ignored", got)
		case want != nil && !ok:
			t.Errorf("line ignored, want %+v", *want)
		case want != nil && !reflect.DeepEqual(got, *want):
			t.Errorf("got %+v, want %+v", got, *want)
		}
	}
	for proxy, cases := range parseCases {
		for _, c := range cases {
			t.Run(proxy+"/"+c.name, func(t *testing.T) { check(t, proxy, c.line, c.want) })
		}
	}
	// Angie writes the same format as nginx.
	for _, c := range parseCases["nginx"] {
		t.Run("angie/"+c.name, func(t *testing.T) { check(t, "angie", c.line, c.want) })
	}
	for _, c := range forgedCases {
		t.Run(c.name, func(t *testing.T) { check(t, c.proxy, c.line, c.want) })
	}
}

func TestParserForUnknownProxy(t *testing.T) {
	for _, name := range []string{"", "none", "apache"} {
		if ParserFor(name) != nil {
			t.Errorf("ParserFor(%q) is not nil", name)
		}
	}
}
