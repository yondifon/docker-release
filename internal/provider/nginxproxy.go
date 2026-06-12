package provider

import (
	"context"
	"crypto/sha1"
	"embed"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/malico/docker-release/internal/health"
)

//go:embed stock/nginx.tmpl
var stockNginxTmpl embed.FS

// NginxProxyUpstreamName computes the upstream name nginx-proxy assigns using
// the same algorithm as the nginx-proxy template:
//   - root path (/):  {VIRTUAL_HOST}
//   - other paths:    {VIRTUAL_HOST}-{sha1(VIRTUAL_PATH)}
func NginxProxyUpstreamName(env []string) (string, error) {
	host := envValue(env, "VIRTUAL_HOST")
	if host == "" {
		return "", fmt.Errorf("VIRTUAL_HOST not set")
	}

	path := envValue(env, "VIRTUAL_PATH")
	if path == "" || path == "/" {
		return host, nil
	}

	sum := sha1.Sum([]byte(path))
	return fmt.Sprintf("%s-%x", host, sum), nil
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix)
		}
	}
	return ""
}

const (
	upstreamMarkerStart = "{{- /* docker-release:managed-start */ -}}"
	upstreamMarkerEnd   = "{{- /* docker-release:managed-end */ -}}"

	originalUpstreamStart = "{{- define \"upstream\" }}"
	originalUpstreamEnd   = "{{- end }}"
)

type NginxProxyProvider struct {
	templatePath string
	stockTmpl    string

	mu       sync.Mutex
	services map[string]*UpstreamState
}

func NewNginxProxy(templatePath string) (*NginxProxyProvider, error) {
	stock, err := stockNginxTmpl.ReadFile("stock/nginx.tmpl")
	if err != nil {
		return nil, fmt.Errorf("reading stock template: %w", err)
	}

	if err := os.WriteFile(templatePath, stock, 0o644); err != nil {
		return nil, fmt.Errorf("seeding nginx template: %w", err)
	}

	return &NginxProxyProvider{
		templatePath: templatePath,
		stockTmpl:    string(stock),
		services:     make(map[string]*UpstreamState),
	}, nil
}

func NewNginxProxyFromString(templatePath string, stockTmpl string) *NginxProxyProvider {
	return &NginxProxyProvider{
		templatePath: templatePath,
		stockTmpl:    stockTmpl,
		services:     make(map[string]*UpstreamState),
	}
}

func (p *NginxProxyProvider) GenerateConfig(ctx context.Context, state *UpstreamState) error {
	if err := state.Validate(); err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.services[state.Service] = state

	return p.writeTemplate()
}

func (p *NginxProxyProvider) RemoveService(service string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.services, service)

	return p.writeTemplate()
}

func (p *NginxProxyProvider) Reload(ctx context.Context) error {
	return nil
}

func (p *NginxProxyProvider) writeTemplate() error {
	tmpl := p.buildTemplate()

	tmp := p.templatePath + ".tmp"
	if err := os.WriteFile(tmp, []byte(tmpl), 0o644); err != nil {
		return fmt.Errorf("writing template: %w", err)
	}

	if err := os.Rename(tmp, p.templatePath); err != nil {
		return fmt.Errorf("renaming template: %w", err)
	}

	health.RecordFile(p.templatePath)
	return nil
}

func (p *NginxProxyProvider) buildTemplate() string {
	if len(p.services) == 0 {
		return p.stockTmpl
	}

	upstreamBlock := extractUpstreamBlock(p.stockTmpl)
	if upstreamBlock == "" {
		return p.stockTmpl
	}

	replacement := buildManagedUpstream(p.services, upstreamBlock)
	return strings.Replace(p.stockTmpl, upstreamBlock, replacement, 1)
}

func extractUpstreamBlock(tmpl string) string {
	start := strings.Index(tmpl, originalUpstreamStart)
	if start == -1 {
		return ""
	}

	searchFrom := start + len(originalUpstreamStart)
	depth := 1

	blockOpeners := []string{
		"{{- define ", "{{ define ",
		"{{- if ", "{{ if ",
		"{{- range ", "{{ range ",
		"{{- with ", "{{ with ",
		"{{- block ", "{{ block ",
	}

	endTags := []string{"{{- end }}", "{{ end }}", "{{- end}}", "{{ end}}"}

	for i := searchFrom; i < len(tmpl); i++ {
		remaining := tmpl[i:]

		for _, opener := range blockOpeners {
			if strings.HasPrefix(remaining, opener) {
				depth++
				break
			}
		}

		for _, endTag := range endTags {
			if strings.HasPrefix(remaining, endTag) {
				depth--
				if depth == 0 {
					end := i + len(endTag)
					return tmpl[start:end]
				}
				break
			}
		}
	}

	return ""
}

func buildManagedUpstream(services map[string]*UpstreamState, originalBlock string) string {
	var b strings.Builder

	b.WriteString(originalUpstreamStart + "\n")
	b.WriteString("    {{- $vpath := .VPath }}\n")
	b.WriteString("    " + upstreamMarkerStart + "\n")

	first := true
	for _, state := range services {
		upstreamName := state.ResolveUpstreamName()

		prefix := "else if"
		if first {
			prefix = "if"
			first = false
		}

		b.WriteString(fmt.Sprintf("    {{- %s eq $vpath.upstream \"%s\" }}\n", prefix, upstreamName))
		b.WriteString(renderNginxProxyUpstream(state))
	}

	b.WriteString("    {{- else }}\n")

	inner := extractInnerUpstream(originalBlock)
	b.WriteString(inner)

	b.WriteString("    {{- end }}\n")
	b.WriteString("    " + upstreamMarkerEnd + "\n")
	b.WriteString(originalUpstreamEnd)

	return b.String()
}

func extractInnerUpstream(block string) string {
	start := strings.Index(block, originalUpstreamStart)
	if start == -1 {
		return block
	}

	inner := block[start+len(originalUpstreamStart):]

	lastEnd := strings.LastIndex(inner, originalUpstreamEnd)
	if lastEnd == -1 {
		return inner
	}

	return inner[:lastEnd]
}

func renderNginxProxyUpstream(state *UpstreamState) string {
	var b strings.Builder

	upstreamName := state.ResolveUpstreamName()
	fmt.Fprintf(&b, "upstream %s {\n", upstreamName)

	if state.Affinity == "ip" || state.Affinity == "cookie" {
		b.WriteString("    ip_hash;\n")
	} else {
		b.WriteString("    least_conn;\n")
	}

	for _, s := range state.Servers {
		if s.Backup && !supportsNginxBackup(state.Affinity) {
			continue
		}

		fmt.Fprintf(&b, "    server %s", s.Addr)

		if s.Down {
			b.WriteString(" down")
		} else if s.Backup {
			b.WriteString(" backup")
		} else if s.Weight > 0 {
			fmt.Fprintf(&b, " weight=%d", s.Weight)
		}

		b.WriteString(";\n")
	}

	if state.Keepalive > 0 {
		fmt.Fprintf(&b, "    keepalive %d;\n", state.Keepalive)
	}

	b.WriteString("}\n")

	return b.String()
}
