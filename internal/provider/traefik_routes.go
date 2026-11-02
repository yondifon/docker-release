package provider

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/malico/docker-release/internal/config"
)

type traefikHTTPConfig map[string]map[string]any

func NewTraefikRoutes(configDir string, route NginxRoute) *TraefikProvider {
	p := NewTraefik(configDir)
	p.bundled = true
	p.route = route
	p.apiURL = "http://127.0.0.1:8080"
	return p
}

func (p *TraefikProvider) generateRoutes(state *UpstreamState) error {
	routeData, err := json.Marshal(p.route)
	if err != nil {
		return err
	}
	if p.route.SSLCert != "" {
		cert, err := os.ReadFile(p.route.SSLCert)
		if err != nil {
			return err
		}
		key, err := os.ReadFile(p.route.SSLKey)
		if err != nil {
			return err
		}
		if _, err := tls.X509KeyPair(cert, key); err != nil {
			return fmt.Errorf("loading Traefik certificate: %w", err)
		}
		routeData = append(routeData, cert...)
		routeData = append(routeData, key...)
	}
	previewLive := p.route.PreviewHost != "" && len(state.Preview) > 0
	if previewLive {
		routeData = append(routeData, "\npreview-live"...)
	}
	linkLive := p.linkLive(state)
	if linkLive {
		routeData = append(routeData, "\npreview-link "+state.PreviewToken...)
	}
	// Names include the complete config, preview servers and token too, so an
	// older API snapshot cannot confirm it.
	revision := sha256.Sum256(append([]byte(renderTraefikYAMLWithPreview(state, p.route.PreviewHost != "" || linkLive)), routeData...))
	name := fmt.Sprintf("dr-%s-%x", state.Service, revision[:12])
	httpConfig := traefikHTTPConfig{"services": traefikServices(name, state)}
	config := map[string]any{"http": httpConfig}
	if p.route.Host != "" || p.route.Path != "" {
		p.addRouters(httpConfig, name)
	}
	if p.route.PreviewLink && (p.route.Host != "" || p.route.Path != "") {
		p.addQuietRouters(httpConfig, name)
	}
	if previewLive || linkLive {
		p.addPreviewRouters(httpConfig, name, state, previewLive, linkLive)
	}
	if linkLive {
		p.addLinkRouters(httpConfig, name, state, previewLive)
	}
	if p.route.SSLCert != "" {
		config["tls"] = map[string]any{"certificates": []any{map[string]string{
			"certFile": p.route.SSLCert, "keyFile": p.route.SSLKey,
		}}}
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	p.pending = map[string][]byte{
		filepath.Join(p.configDir, state.Service+".yml"): append([]byte(generatedHeader), data...),
	}
	return nil
}

func traefikServices(name string, state *UpstreamState) map[string]any {
	services := make(map[string]any)
	if !hasWeightedServers(state) {
		services[name] = traefikLoadBalancer(state.Servers, state.Affinity, stickyCookieName(state))
		return services
	}
	groups := map[string][]Server{}
	var order []string
	for _, server := range state.Servers {
		if server.Down || server.Backup || server.Weight <= 0 {
			continue
		}
		group := server.Group
		if group == "" {
			group = fmt.Sprint(server.Weight)
		}
		if _, ok := groups[group]; !ok {
			order = append(order, group)
		}
		groups[group] = append(groups[group], server)
	}
	children := make([]any, 0, len(order))
	for i, group := range order {
		child := fmt.Sprintf("%s-%d", name, i)
		services[child] = traefikLoadBalancer(groups[group], state.Affinity, stickyCookieName(state)+"_backend")
		children = append(children, map[string]any{"name": child, "weight": groups[group][0].Weight})
	}
	balancer := map[string]any{"services": children}
	kind := "weighted"
	if state.Affinity == "ip" {
		kind = "highestRandomWeight"
	} else if state.Affinity == "cookie" {
		balancer["sticky"] = map[string]any{"cookie": map[string]string{"name": stickyCookieName(state)}}
	}
	services[name] = map[string]any{kind: balancer}
	return services
}

func traefikLoadBalancer(servers []Server, affinity, cookie string) map[string]any {
	addresses := make([]any, 0, len(servers))
	for _, server := range servers {
		if !server.Down && !server.Backup {
			addresses = append(addresses, map[string]string{"url": "http://" + server.Addr})
		}
	}
	balancer := map[string]any{"servers": addresses}
	if affinity != "" {
		balancer["sticky"] = map[string]any{"cookie": map[string]string{"name": cookie}}
	}
	return map[string]any{"loadBalancer": balancer}
}

func traefikRouteRule(route NginxRoute) string {
	var rules []string
	for _, host := range strings.Split(route.Host, ",") {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		if strings.Contains(host, "*") {
			pattern := strings.ReplaceAll(regexp.QuoteMeta(host), `\*`, `.*`)
			rules = append(rules, fmt.Sprintf("HostRegexp(`^%s$`)", pattern))
		} else {
			rules = append(rules, fmt.Sprintf("Host(`%s`)", host))
		}
	}
	rule := ""
	if len(rules) > 0 {
		rule = "(" + strings.Join(rules, " || ") + ")"
	}
	path := route.Path
	if path == "" {
		path = "/"
	}
	if rule != "" {
		rule += " && "
	}
	return rule + fmt.Sprintf("PathPrefix(`%s`)", path)
}

func (p *TraefikProvider) addRouters(config traefikHTTPConfig, name string) {
	rule := traefikRouteRule(p.route)
	middlewares := map[string]any{}
	var strip []string
	if path := p.route.Path; path != "" && path != "/" {
		strip = []string{name + "-strip"}
		middlewares[strip[0]] = map[string]any{"stripPrefix": map[string]any{"prefixes": []string{strings.TrimSuffix(path, "/")}}}
	}
	httpRouter := map[string]any{"entryPoints": []string{"web"}, "rule": rule, "service": name}
	if len(strip) > 0 {
		httpRouter["middlewares"] = strip
	}
	routers := map[string]any{name + "-http": httpRouter}
	if p.route.SSLCert != "" {
		httpsRouter := map[string]any{"entryPoints": []string{"websecure"}, "rule": rule, "service": name, "tls": map[string]any{}}
		if len(strip) > 0 {
			httpsRouter["middlewares"] = strip
		}
		routers[name+"-https"] = httpsRouter
		if p.route.SSLRedirect {
			config["services"][name+"-redirect"] = traefikLoadBalancer([]Server{{Addr: "127.0.0.1:8082"}}, "", "")
			httpRouter["service"] = name + "-redirect"
			delete(httpRouter, "middlewares")
		}
	}
	config["routers"] = routers
	if len(middlewares) > 0 {
		config["middlewares"] = middlewares
	}
}

// previewPriority puts the preview host above every other route, so a wildcard or
// path-only main route cannot answer for it.
const previewPriority = 1_000_000

// addPreviewRouters adds the `-preview` service, and the routers that send the
// preview host to it. It runs after `addRouters` and adds to the routers it made.
// With a live preview link the host serves the preview servers only to requests
// that carry the token.
func (p *TraefikProvider) addPreviewRouters(config traefikHTTPConfig, name string, state *UpstreamState, hostLive, linkLive bool) {
	service := name + "-preview"
	config["services"][service] = traefikLoadBalancer(state.Preview, "", "")
	if !hostLive {
		return
	}
	rule := fmt.Sprintf("Host(`%s`)", p.route.PreviewHost)
	serving := rule
	if linkLive {
		serving = rule + " && " + traefikTokenRule(state.PreviewToken)
	}
	routers := config["routers"]
	if routers == nil {
		routers = map[string]any{}
		config["routers"] = routers
	}
	httpRouter := map[string]any{"entryPoints": []string{"web"}, "rule": serving, "service": service, "priority": previewPriority}
	routers[service+"-http"] = httpRouter
	if p.route.SSLCert == "" {
		return
	}
	routers[service+"-https"] = map[string]any{
		"entryPoints": []string{"websecure"}, "rule": serving, "service": service,
		"priority": previewPriority, "tls": map[string]any{},
	}
	if p.route.SSLRedirect {
		config["services"][service+"-redirect"] = traefikLoadBalancer([]Server{{Addr: "127.0.0.1:8082"}}, "", "")
		httpRouter["service"] = service + "-redirect"
		httpRouter["rule"] = rule
	}
}

// The token routers sit just under the preview host, and the link routers above
// both, so a request that carries the cookie and the link still gets the link
// answer.
const (
	previewTokenPriority = previewPriority - 1
	previewLinkPriority  = previewPriority + 1
)

// addQuietRouters copies the main routers for requests that carry the link
// parameter with any value, and turns their access log off. The log holds the
// whole URL, so a link sent when it does nothing (before or after the release)
// would write the token to it. The requests still reach the app untouched.
// `QueryRegexp` is needed because `Query` with only a name matches an empty value.
// It runs after `addRouters` and copies the routers it made.
func (p *TraefikProvider) addQuietRouters(cfg traefikHTTPConfig, name string) {
	routers := cfg["routers"]
	rule := traefikRouteRule(p.route) + fmt.Sprintf(" && QueryRegexp(`%s`, `.*`)", config.PreviewQueryParam)
	for _, suffix := range []string{"http", "https"} {
		main, ok := routers[name+"-"+suffix].(map[string]any)
		if !ok {
			continue
		}
		quiet := make(map[string]any, len(main)+3)
		for key, value := range main {
			quiet[key] = value
		}
		quiet["rule"] = rule
		quiet["priority"] = previewTokenPriority - 1
		quiet["observability"] = map[string]any{"accessLogs": false}
		routers[name+"-quiet-"+suffix] = quiet
	}
}

// linkLive reports whether the preview link works in this config: the label is
// set and green waits with a live token.
func (p *TraefikProvider) linkLive(state *UpstreamState) bool {
	return p.route.PreviewLink && state.PreviewToken != "" && len(state.Preview) > 0
}

// traefikTokenRule matches a request that carries the token in the preview
// header, or as one cookie among the others.
func traefikTokenRule(token string) string {
	cookie := `(^|;\s*)` + config.PreviewCookie + "=" + token + `\s*(;|$)`
	return fmt.Sprintf("(Header(`%s`, `%s`) || HeaderRegexp(`Cookie`, `%s`))", config.PreviewHeader, token, cookie)
}

// traefikLinkRedirects answers the link request: one regex for each place the
// parameter can sit in the query. A regex that does not match passes the request
// on, so only the one that fits answers. The last one always matches and drops the
// query, so a request that Traefik takes for a link but the others miss (an
// encoded name) is never sent to the app. Each regex is matched against the full URL.
func traefikLinkRedirects(token string) []map[string]any {
	param := regexp.QuoteMeta(config.PreviewQueryParam + "=" + token)
	start := `^[^:/]+://[^/?]+([^?]*)`
	cases := [][2]string{
		{start + `\?` + param + `$`, "${1}"},
		{start + `\?` + param + `&(.*)$`, "${1}?${2}"},
		{start + `\?(.*)&` + param + `$`, "${1}?${2}"},
		{start + `\?(.*)&` + param + `&(.*)$`, "${1}?${2}&${3}"},
		{start + `.*$`, "${1}"},
	}
	out := make([]map[string]any, 0, len(cases))
	for _, c := range cases {
		out = append(out, map[string]any{"redirectRegex": map[string]any{
			"regex": c[0], "replacement": c[1], "permanent": false,
		}})
	}
	return out
}

// addLinkRouters adds the routers for the preview token. A request with the
// cookie or the header goes to the preview servers only. A request with the link
// parameter gets the cookie and a redirect that drops the parameter, and never
// reaches the app. Neither runs on plain HTTP when that port only redirects to
// HTTPS. The link requests are kept out of the access log, because their URL
// holds the token.
func (p *TraefikProvider) addLinkRouters(cfg traefikHTTPConfig, name string, state *UpstreamState, hostLive bool) {
	token := state.PreviewToken
	service := name + "-preview"
	secure := p.route.SSLCert != ""
	type entry struct {
		suffix, entryPoint, cookie string
		tls                        bool
	}
	var entries []entry
	cookie := fmt.Sprintf("%s=%s; Path=/; HttpOnly; SameSite=Lax", config.PreviewCookie, token)
	middlewares := cfg["middlewares"]
	if middlewares == nil {
		middlewares = map[string]any{}
		cfg["middlewares"] = middlewares
	}
	cookieMiddleware := func(id, value string) string {
		middlewares[id] = map[string]any{"headers": map[string]any{
			"customResponseHeaders": map[string]string{"Set-Cookie": value},
		}}
		return id
	}
	if !secure || !p.route.SSLRedirect {
		entries = append(entries, entry{"http", "web", cookieMiddleware(name+"-link-cookie", cookie), false})
	}
	if secure {
		entries = append(entries, entry{"https", "websecure", cookieMiddleware(name+"-link-cookie-secure", cookie+"; Secure"), true})
	}
	var answer []string
	for i, redirect := range traefikLinkRedirects(token) {
		id := fmt.Sprintf("%s-link-redirect-%d", name, i)
		middlewares[id] = redirect
		answer = append(answer, id)
	}

	routers := cfg["routers"]
	if routers == nil {
		routers = map[string]any{}
		cfg["routers"] = routers
	}
	add := func(id, rule, svc string, priority int, chain []string, e entry) {
		router := map[string]any{
			"entryPoints": []string{e.entryPoint}, "rule": rule, "service": svc, "priority": priority,
		}
		if len(chain) > 0 {
			router["middlewares"] = chain
		}
		if e.tls {
			router["tls"] = map[string]any{}
		}
		if svc == "noop@internal" {
			router["observability"] = map[string]any{"accessLogs": false}
		}
		routers[id+"-"+e.suffix] = router
	}
	query := fmt.Sprintf("Query(`%s`, `%s`)", config.PreviewQueryParam, token)
	for _, e := range entries {
		chain := append([]string{e.cookie}, answer...)
		if p.route.Host != "" || p.route.Path != "" {
			rule := traefikRouteRule(p.route)
			var strip []string
			if path := p.route.Path; path != "" && path != "/" {
				strip = []string{name + "-strip"}
			}
			add(name+"-token", rule+" && "+traefikTokenRule(token), service, previewTokenPriority, strip, e)
			add(name+"-link", rule+" && "+query, "noop@internal", previewLinkPriority, chain, e)
		}
		if hostLive {
			add(name+"-hostlink", fmt.Sprintf("Host(`%s`) && %s", p.route.PreviewHost, query), "noop@internal", previewLinkPriority, chain, e)
		}
	}
}

func (p *TraefikProvider) RemoveStaleConfigs(ctx context.Context, active map[string]bool, canRemove func(string) bool) error {
	unlock, err := lockProxyConfigs(ctx, p.configDir, ".traefik.lock")
	if err != nil {
		return err
	}
	defer unlock()
	changes, err := staleConfigChanges(map[string][]string{p.configDir: {".yml"}}, active, canRemove)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}
	return p.applyRoutesLocked(ctx, changes)
}
