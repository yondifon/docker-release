package accesslog

import (
	"encoding/json"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// ParserFor returns the parser for the access log of a bundled proxy, or nil
// when the proxy has none.
//
// Every parser ignores a line unless it names an app container, so requests the
// proxy answers itself (the health route, an unknown host) are skipped. The
// `nginx`, `angie` and `haproxy` bundles end each line with a `drlog` trailer
// that quotes every value taken from the client, so client text can't pass as
// part of the trailer. `caddy` and `traefik` write JSON, where client text
// stays inside its own string.
func ParserFor(proxy string) Parser {
	switch proxy {
	case "nginx", "angie":
		return parseNginx
	case "haproxy":
		return parseHAProxy
	case "caddy":
		return parseCaddy
	case "traefik":
		return parseTraefik
	}
	return nil
}

// The trailer is the end of the line and quotes its values, so the first match
// from the left is the real one: text a client sends can't hold an unescaped
// quote.
var (
	nginxTrailer   = regexp.MustCompile(` drlog status=(\d{3}) upstream="([^"]*)" host="([^"]*)" path="([^"]*)"$`)
	nginxErrorLine = regexp.MustCompile(`^\d{4}/\d\d/\d\d \d\d:\d\d:\d\d \[`)
	haproxyTrailer = regexp.MustCompile(` drlog status=(\d{3}) upstream="([^"]*)" host="((?:[^"\\]|\\.)*)" path="((?:[^"\\]|\\.)*)"$`)
)

// parseNginx reads a line of the nginx or Angie `main` log format. Both escape
// `"`, `\` and control bytes in `$host` and `$request_uri` as `\xHH`.
func parseNginx(line string) (Entry, bool) {
	// An error log line starts with a date and quotes the request, so it can
	// end the way a trailer does.
	if nginxErrorLine.MatchString(line) {
		return Entry{}, false
	}
	m := nginxTrailer.FindStringSubmatch(line)
	if m == nil {
		return Entry{}, false
	}
	upstream := m[2]
	// A retried request lists every server it tried, separated by `, `. A request
	// sent on by an internal redirect adds groups separated by ` : `. The last
	// server is the one that answered.
	if i := strings.LastIndex(upstream, ","); i >= 0 {
		upstream = upstream[i+1:]
	}
	if i := strings.LastIndex(upstream, " : "); i >= 0 {
		upstream = upstream[i+3:]
	}
	return newEntry(m[1], upstream, unescapeNginx(m[3]), unescapeNginx(m[4]))
}

// parseHAProxy reads a line of the `log-format` of the generated frontend. The
// host and path are escaped like a JSON string.
func parseHAProxy(line string) (Entry, bool) {
	m := haproxyTrailer.FindStringSubmatch(line)
	if m == nil {
		return Entry{}, false
	}
	return newEntry(m[1], m[2], unescapeJSON(m[3]), unescapeJSON(m[4]))
}

// parseCaddy reads a line of Caddy's JSON access log. The bundle adds the
// top-level `upstream` field; a client can't write a top-level field.
func parseCaddy(line string) (Entry, bool) {
	var l struct {
		Status   int    `json:"status"`
		Upstream string `json:"upstream"`
		Request  struct {
			Host string `json:"host"`
			URI  string `json:"uri"`
		} `json:"request"`
	}
	if !decodeLine(line, &l) {
		return Entry{}, false
	}
	return newEntry(strconv.Itoa(l.Status), l.Upstream, l.Request.Host, l.Request.URI)
}

// parseTraefik reads a line of Traefik's JSON access log. Requests Traefik
// answers itself have no `ServiceAddr`.
func parseTraefik(line string) (Entry, bool) {
	var l struct {
		Status      int    `json:"DownstreamStatus"`
		ServiceAddr string `json:"ServiceAddr"`
		Host        string `json:"RequestHost"`
		Path        string `json:"RequestPath"`
	}
	if !decodeLine(line, &l) {
		return Entry{}, false
	}
	return newEntry(strconv.Itoa(l.Status), l.ServiceAddr, l.Host, l.Path)
}

func decodeLine(line string, v any) bool {
	if !strings.HasPrefix(line, "{") {
		return false
	}
	return json.Unmarshal([]byte(line), v) == nil
}

// newEntry checks and cleans the fields every parser reads. It reports false
// when the line names no app container.
func newEntry(status, upstream, host, path string) (Entry, bool) {
	code, err := strconv.Atoi(status)
	if err != nil || code < 100 || code > 599 {
		return Entry{}, false
	}
	upstream = strings.TrimSpace(upstream)
	if _, after, ok := strings.Cut(upstream, "://"); ok {
		upstream = after
	}
	upstream = strings.TrimSuffix(upstream, "/")
	// `ip:port`, as the controller writes it. `-`, `-:-` and empty are no server.
	h, port, err := net.SplitHostPort(upstream)
	if err != nil || h == "" || port == "" || h == "-" || port == "-" {
		return Entry{}, false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	return Entry{Status: code, Upstream: upstream, Host: strings.ToLower(host), Path: path}, true
}

// unescapeNginx reverses nginx's default log escaping, `\xHH` for one byte.
func unescapeNginx(s string) string {
	if !strings.Contains(s, `\x`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] == 'x' {
			if n, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// unescapeJSON reverses the escaping of HAProxy's `json` converter. A value it
// can't read stays as written.
func unescapeJSON(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var out string
	if err := json.Unmarshal([]byte(`"`+s+`"`), &out); err != nil {
		return s
	}
	return out
}
