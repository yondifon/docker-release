package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/docker"
)

type HAProxyRoute struct {
	Host string
	// PreviewHost answers with the servers in `UpstreamState.Preview`. The saved
	// `.route` file keeps it only while the preview is live, along with the
	// `PreviewBackend` it points at.
	PreviewHost    string `json:",omitempty"`
	PreviewBackend string `json:",omitempty"`
	// PreviewLink is set when `release.preview.token` is set. The route then sends
	// requests that carry `UpstreamState.PreviewToken` to `UpstreamState.Preview`.
	PreviewLink bool `json:",omitempty"`
	// PreviewToken and LinkBackend are saved only while the token is live.
	// `LinkBackend` holds the preview servers, behind the same path rewrite as
	// the main backend.
	PreviewToken string `json:",omitempty"`
	LinkBackend  string `json:",omitempty"`
	// PreviewOnly marks the preview host's own route when a token is live: it
	// answers the token requests and leaves every other request to the other routes.
	PreviewOnly bool `json:"-"`
	Path        string
	TLSCert     string
	TLSKey      string
	TLSRedirect bool
	Backend     string
	Affinity    string
	TLSHash     string
}

func NewHAProxyRoutes(configDir string, route HAProxyRoute, dockerClient *docker.Client, serviceName, project string) *HAProxyProvider {
	p := NewHAProxy(configDir, dockerClient, serviceName, project)
	p.bundled = true
	p.route = route
	return p
}

func (p *HAProxyProvider) generateBundledConfig(state *UpstreamState) error {
	route := p.route
	route.Backend = state.ResolveUpstreamName() + "_be"
	preview := route.PreviewHost != "" && len(state.Preview) > 0
	link := route.PreviewLink && state.PreviewToken != "" && len(state.Preview) > 0
	rewrite := route.Path != "" && route.Path != "/"
	previewName := state.previewState().ResolveUpstreamName()
	route.PreviewHost = ""
	if preview {
		route.PreviewHost = p.route.PreviewHost
		route.PreviewBackend = previewName + "_be"
	}
	if link {
		route.PreviewToken = state.PreviewToken
		route.LinkBackend = previewName + "_be"
		if rewrite {
			route.LinkBackend = previewName + "_link_be"
		}
	}
	if route.TLSCert != "" {
		cert, err := os.ReadFile(route.TLSCert)
		if err != nil {
			return err
		}
		key, err := os.ReadFile(route.TLSKey)
		if err != nil {
			return err
		}
		route.TLSHash = fmt.Sprintf("%x", sha256.Sum256(append(cert, key...)))
	}
	data, err := json.Marshal(route)
	if err != nil {
		return err
	}
	backend := *state
	backend.Affinity = route.Affinity
	conf := renderHAProxyBundledBackend(&backend)
	if rewrite {
		conf += haproxyPathRewrite(route.Path)
	}
	if preview || (link && !rewrite) {
		// The preview host always serves `/`, so this backend has no path rewrite.
		previewBackend := *state.previewState()
		previewBackend.Affinity = route.Affinity
		conf += "\n" + strings.TrimPrefix(renderHAProxyBundledBackend(&previewBackend), haproxyHeader)
	}
	if link && rewrite {
		linkBackend := *state.previewState()
		linkBackend.UpstreamName += "_link"
		linkBackend.Affinity = route.Affinity
		conf += "\n" + strings.TrimPrefix(renderHAProxyBundledBackend(&linkBackend), haproxyHeader) + haproxyPathRewrite(route.Path)
	}
	p.pending = map[string][]byte{
		filepath.Join(p.configDir, state.Service+".cfg"):   []byte(conf),
		filepath.Join(p.configDir, state.Service+".route"): append([]byte(generatedHeader), data...),
	}
	return nil
}

func haproxyPathRewrite(path string) string {
	prefix := regexp.QuoteMeta(strings.TrimSuffix(path, "/"))
	return fmt.Sprintf("    http-request replace-path ^%s/?(.*)$ /\\1\n", prefix)
}

func (p *HAProxyProvider) applyBundledConfigs(ctx context.Context, changes map[string][]byte) error {
	unlock, err := lockProxyConfigs(ctx, p.configDir, ".haproxy.lock")
	if err != nil {
		return err
	}
	defer unlock()
	return p.applyBundledConfigsLocked(ctx, changes)
}

func (p *HAProxyProvider) applyBundledConfigsLocked(ctx context.Context, changes map[string][]byte) error {
	if changes == nil {
		changes = make(map[string][]byte)
	}
	routes, err := p.bundledRoutes(changes)
	if err != nil {
		return err
	}
	conf, err := renderHAProxyFrontend(routes)
	if err != nil {
		return err
	}
	changes[filepath.Join(p.configDir, "routes", "frontend.cfg")] = []byte(conf)
	previous := make(map[string][]byte)
	runtimeOnly := len(p.pending) > 0
	for path, data := range changes {
		old, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if strings.HasSuffix(path, ".cfg") && filepath.Dir(path) == p.configDir {
			previous[path] = old
			if haproxyBackendOptions(old) != haproxyBackendOptions(data) {
				runtimeOnly = false
			}
		} else if !bytes.Equal(old, data) {
			runtimeOnly = false
		}
	}
	return applyProxyConfigFiles(ctx, changes, func(ctx context.Context) error {
		bootstrap, err := bundledBootstrapping(ctx, "haproxy")
		if err != nil {
			return err
		}
		if err := p.validateBundledConfig(ctx); err != nil {
			return err
		}
		if bootstrap {
			return nil
		}
		if !runtimeOnly {
			return p.reloadBundled(ctx)
		}
		for path, old := range previous {
			if err := updateHAProxyBackend(ctx, changes[path]); err != nil {
				restoreCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return errors.Join(err, updateHAProxyBackend(restoreCtx, old))
			}
		}
		return nil
	})
}

func (p *HAProxyProvider) bundledRoutes(changes map[string][]byte) ([]HAProxyRoute, error) {
	files, err := proxyConfigFilesByService(map[string][]string{p.configDir: {".route"}})
	if err != nil {
		return nil, err
	}
	all := make(map[string][]byte)
	for _, paths := range files {
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			all[path] = data
		}
	}
	for path, data := range changes {
		if strings.HasSuffix(path, ".route") {
			all[path] = data
		}
	}
	var routes []HAProxyRoute
	for path, data := range all {
		if data == nil {
			continue
		}
		var route HAProxyRoute
		if err := json.Unmarshal([]byte(strings.TrimPrefix(string(data), generatedHeader)), &route); err != nil {
			return nil, fmt.Errorf("reading HAProxy route %s: %w", path, err)
		}
		if route.PreviewHost != "" && route.PreviewBackend != "" {
			// Same certificate as the main host, so the preview route loads none.
			entry := HAProxyRoute{Host: route.PreviewHost, Backend: route.PreviewBackend, TLSRedirect: route.TLSRedirect}
			if route.PreviewToken != "" {
				entry.PreviewToken, entry.LinkBackend, entry.PreviewOnly = route.PreviewToken, route.PreviewBackend, true
			}
			routes = append(routes, entry)
		}
		if route.Host != "" || route.Path != "" {
			routes = append(routes, route)
		}
	}
	sort.Slice(routes, func(i, j int) bool {
		if (routes[i].Host != "") != (routes[j].Host != "") {
			return routes[i].Host != ""
		}
		if len(routes[i].Path) != len(routes[j].Path) {
			return len(routes[i].Path) > len(routes[j].Path)
		}
		return routes[i].Backend < routes[j].Backend
	})
	return routes, nil
}

func renderHAProxyFrontend(routes []HAProxyRoute) (string, error) {
	var b strings.Builder
	b.WriteString("# Generated by docker-release for HAProxy\n")
	hosts := make(map[string]bool)
	namedHosts := false
	logFormat := haproxyLogFormat
	var certs []string
	for i, route := range routes {
		if route.PreviewLink {
			logFormat = haproxyLogFormatWithoutQuery
		}
		for _, host := range strings.Split(route.Host, ",") {
			host = strings.ToLower(strings.TrimSpace(host))
			if host == "" {
				continue
			}
			if hosts[host] {
				return "", fmt.Errorf("haproxy host %q is already used; use a different host or a custom frontend", host)
			}
			hosts[host] = true
		}
		if route.TLSCert != "" {
			fmt.Fprintf(&b, "# TLS %s\n", route.TLSHash)
			fmt.Fprintf(&b, "crt-store dr_%d\n    load crt %s key %s alias app\n\n", i, route.TLSCert, route.TLSKey)
			certs = append(certs, fmt.Sprintf("crt @dr_%d/app", i))
		}
	}
	b.WriteString("frontend docker_release\n    bind :80\n")
	b.WriteString(logFormat)
	if len(certs) > 0 {
		fmt.Fprintf(&b, "    bind :443 ssl %s\n", strings.Join(certs, " "))
	}
	b.WriteString("    http-request return status 200 content-type text/plain string ok if { path /health } !{ ssl_fc }\n")
	b.WriteString("    http-request set-header X-Forwarded-Proto https if { ssl_fc }\n    http-request set-header X-Forwarded-Proto http unless { ssl_fc }\n")
	for i, route := range routes {
		if route.Host != "" {
			hosts := strings.Fields(strings.ReplaceAll(route.Host, ",", " "))
			fmt.Fprintf(&b, "    acl host_%d req.hdr(host),host_only -i %s\n", i, strings.Join(hosts, " "))
			if !route.PreviewOnly {
				fmt.Fprintf(&b, "    acl named_host req.hdr(host),host_only -i %s\n", strings.Join(hosts, " "))
				namedHosts = true
			}
		}
		if route.Path != "" && route.Path != "/" {
			fmt.Fprintf(&b, "    acl path_%d path_beg %s\n", i, route.Path)
		}
		if route.PreviewToken != "" {
			writeHAProxyPreviewACLs(&b, i, route)
		}
	}
	for i, route := range routes {
		condition := haproxyRouteCondition(route, i)
		if route.Host == "" && namedHosts {
			condition += " !named_host"
		}
		if route.PreviewToken != "" {
			// The link carries the token in its URL, so no log line may be written for it.
			fmt.Fprintf(&b, "    http-request set-log-level silent if %s pb_%d\n", condition, i)
		}
		if route.TLSRedirect {
			redirect := condition
			if route.PreviewOnly {
				redirect += fmt.Sprintf(" pany_%d", i)
			}
			fmt.Fprintf(&b, "    http-request redirect location https://%%[req.hdr(host),host_only]%%[pathq] code 308 if %s !{ ssl_fc }\n", redirect)
		}
		if route.PreviewToken != "" {
			writeHAProxyPreviewRules(&b, i, route, condition)
		}
		if !route.PreviewOnly {
			fmt.Fprintf(&b, "    http-request set-var(txn.backend) str(%s) if !{ var(txn.backend) -m found } %s\n", route.Backend, condition)
		}
	}
	b.WriteString("    http-request return status 404 unless { var(txn.backend) -m found }\n    use_backend %[var(txn.backend)]\n")
	return b.String(), nil
}

// writeHAProxyPreviewACLs defines the three ways to ask for the preview servers:
// `pa_N` is a cookie or the header, `pb_N` is the link, and `pany_N` is any of them.
// The token has only letters, digits, `_` and `-`, so it needs no escaping.
func writeHAProxyPreviewACLs(b *strings.Builder, i int, route HAProxyRoute) {
	cookie := fmt.Sprintf("req.cook(%s) -m str %s", config.PreviewCookie, route.PreviewToken)
	header := fmt.Sprintf("req.hdr(%s) -m str %s", config.PreviewHeader, route.PreviewToken)
	link := fmt.Sprintf("query -m reg '(^|&)%s=%s(&|$)'", config.PreviewQueryParam, route.PreviewToken)
	fmt.Fprintf(b, "    acl pa_%d %s\n    acl pa_%d %s\n    acl pb_%d %s\n", i, cookie, i, header, i, link)
	if route.PreviewOnly {
		fmt.Fprintf(b, "    acl pany_%d %s\n    acl pany_%d %s\n    acl pany_%d %s\n", i, cookie, i, header, i, link)
	}
}

// writeHAProxyPreviewRules answers the link with a redirect that sets the cookie
// and drops the token from the URL, then sends cookie and header requests to the
// preview servers. Both rules skip a request that an earlier route already took.
// A request for the preview servers never falls back to the main servers: if
// none is up, HAProxy answers with its own 503.
func writeHAProxyPreviewRules(b *strings.Builder, i int, route HAProxyRoute, condition string) {
	param := config.PreviewQueryParam + "=" + route.PreviewToken
	// Four cases, because the parameter can be alone, first, last or in the middle.
	// Then `?` goes back in front, unless nothing is left.
	query := fmt.Sprintf(`"query,regsub('^%[1]s\$',''),regsub('^%[1]s&',''),regsub('&%[1]s\$',''),regsub('&%[1]s&','&'),regsub('^(.+)\$','?\\1')"`, param)
	free := "!{ var(txn.backend) -m found }"
	fmt.Fprintf(b, "    http-request set-var(txn.dr_q) %s if %s pb_%d %s\n", query, condition, i, free)
	cookie := fmt.Sprintf("%s=%s; Path=/; HttpOnly; SameSite=Lax", config.PreviewCookie, route.PreviewToken)
	for _, secure := range []bool{false, true} {
		attrs, tls := cookie, "!{ ssl_fc }"
		if secure {
			attrs, tls = cookie+"; Secure", "{ ssl_fc }"
		}
		fmt.Fprintf(b, "    http-request return status 302 hdr set-cookie \"%s\" hdr location \"%%[path]%%[var(txn.dr_q)]\" hdr cache-control no-store if %s pb_%d %s %s\n", attrs, condition, i, free, tls)
	}
	fmt.Fprintf(b, "    http-request set-var(txn.backend) str(%s) if %s pa_%d %s\n", route.LinkBackend, condition, i, free)
}

// haproxyLogFormat adds a `drlog` trailer to each access log line for the
// controller to read. The `Host` header is captured first, so the log reads it
// as `capture.req.hdr(0)`. Host and path go through `json`, so a quote or a
// backslash in client text can't end the quoted value early.
const haproxyLogFormat = "    capture request header Host len 255\n" + `    log-format "%ci:%cp [%tr] %ft %b/%s %ST %B %{+Q}r drlog status=%ST upstream=\"%si:%sp\" host=\"%[capture.req.hdr(0),json]\" path=\"%[capture.req.uri,field(1,?),json]\""` + "\n"

// haproxyLogFormatWithoutQuery is used while any route has a preview link. It
// logs the request line without the query string, so a token sent in a URL
// never reaches the log, whether it is live or not. The path goes through `json`
// like the trailer's values.
var haproxyLogFormatWithoutQuery = strings.Replace(haproxyLogFormat, "%{+Q}r", `\"%HM %[capture.req.uri,field(1,?),json] %HV\"`, 1)

func haproxyRouteCondition(route HAProxyRoute, i int) string {
	var conditions []string
	if route.Host != "" {
		conditions = append(conditions, fmt.Sprintf("host_%d", i))
	}
	if route.Path != "" && route.Path != "/" {
		conditions = append(conditions, fmt.Sprintf("path_%d", i))
	}
	if len(conditions) == 0 {
		return "TRUE"
	}
	return strings.Join(conditions, " ")
}

func (p *HAProxyProvider) RemoveStaleConfigs(ctx context.Context, active map[string]bool, canRemove func(string) bool) error {
	unlock, err := lockProxyConfigs(ctx, p.configDir, ".haproxy.lock")
	if err != nil {
		return err
	}
	defer unlock()
	changes, err := staleConfigChanges(map[string][]string{p.configDir: {".cfg", ".route"}}, active, canRemove)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		if _, err := os.Stat(filepath.Join(p.configDir, "routes", "frontend.cfg")); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return p.applyBundledConfigsLocked(ctx, changes)
}
