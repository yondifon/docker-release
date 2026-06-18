//go:build bundled_nginx

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const bundledNginxConfigPath = "/etc/nginx/nginx.conf"

type bundledNginxConfig struct {
	HTTPPort      int
	HTTPSPort     int
	ServerName    string
	SSLCert       string
	SSLKey        string
	RedirectHTTPS bool
}

func prepareBundledNginxConfig() error {
	if truthy(os.Getenv("DR_NGINX_SKIP_CONFIG")) {
		return nil
	}

	cfg, err := bundledNginxConfigFromEnv()
	if err != nil {
		return err
	}

	return os.WriteFile(bundledNginxConfigPath, []byte(renderBundledNginxConfig(cfg)), 0o644)
}

func bundledNginxConfigFromEnv() (bundledNginxConfig, error) {
	cfg := bundledNginxConfig{
		HTTPPort:   80,
		HTTPSPort:  443,
		ServerName: envOr("DR_NGINX_SERVER_NAME", "_"),
		SSLCert:    strings.TrimSpace(os.Getenv("DR_NGINX_SSL_CERT")),
		SSLKey:     strings.TrimSpace(os.Getenv("DR_NGINX_SSL_KEY")),
	}

	var err error
	cfg.HTTPPort, err = envPort("DR_NGINX_HTTP_PORT", cfg.HTTPPort)
	if err != nil {
		return bundledNginxConfig{}, err
	}
	cfg.HTTPSPort, err = envPort("DR_NGINX_HTTPS_PORT", cfg.HTTPSPort)
	if err != nil {
		return bundledNginxConfig{}, err
	}
	cfg.RedirectHTTPS = truthy(os.Getenv("DR_NGINX_REDIRECT_HTTPS"))

	for name, value := range map[string]string{
		"DR_NGINX_SERVER_NAME": cfg.ServerName,
		"DR_NGINX_SSL_CERT":    cfg.SSLCert,
		"DR_NGINX_SSL_KEY":     cfg.SSLKey,
	} {
		if err := safeNginxValue(name, value); err != nil {
			return bundledNginxConfig{}, err
		}
	}

	if cfg.RedirectHTTPS && !cfg.sslEnabled() {
		return bundledNginxConfig{}, fmt.Errorf("DR_NGINX_REDIRECT_HTTPS requires DR_NGINX_SSL_CERT and DR_NGINX_SSL_KEY")
	}

	return cfg, nil
}

func (c bundledNginxConfig) sslEnabled() bool {
	return c.SSLCert != "" && c.SSLKey != ""
}

func renderBundledNginxConfig(cfg bundledNginxConfig) string {
	var b strings.Builder

	b.WriteString("user nginx;\n")
	b.WriteString("worker_processes auto;\n")
	b.WriteString("error_log /dev/stderr notice;\n")
	b.WriteString("pid /run/nginx/nginx.pid;\n\n")
	b.WriteString("events {\n")
	b.WriteString("    worker_connections 1024;\n")
	b.WriteString("}\n\n")
	b.WriteString("http {\n")
	b.WriteString("    include /etc/nginx/mime.types;\n")
	b.WriteString("    default_type application/octet-stream;\n\n")
	b.WriteString("    log_format main '$remote_addr - $remote_user [$time_local] \"$request\" '\n")
	b.WriteString("                    '$status $body_bytes_sent \"$http_referer\" '\n")
	b.WriteString("                    '\"$http_user_agent\" \"$http_x_forwarded_for\"';\n")
	b.WriteString("    access_log /dev/stdout main;\n\n")
	b.WriteString("    sendfile on;\n")
	b.WriteString("    keepalive_timeout 65;\n\n")
	b.WriteString("    include /shared/nginx-config/*.conf;\n")
	b.WriteString("    include /etc/docker-release/nginx/http.d/*.conf;\n")
	b.WriteString("    include /etc/docker-release/nginx/conf.d/*.conf;\n\n")

	renderBundledNginxServer(&b, cfg, false)
	if cfg.sslEnabled() {
		b.WriteString("\n")
		renderBundledNginxServer(&b, cfg, true)
	}

	b.WriteString("}\n")
	return b.String()
}

func renderBundledNginxServer(b *strings.Builder, cfg bundledNginxConfig, ssl bool) {
	b.WriteString("    server {\n")
	if ssl {
		fmt.Fprintf(b, "        listen %d ssl default_server;\n", cfg.HTTPSPort)
	} else {
		fmt.Fprintf(b, "        listen %d default_server;\n", cfg.HTTPPort)
	}
	fmt.Fprintf(b, "        server_name %s;\n\n", cfg.ServerName)

	if ssl {
		fmt.Fprintf(b, "        ssl_certificate %s;\n", cfg.SSLCert)
		fmt.Fprintf(b, "        ssl_certificate_key %s;\n", cfg.SSLKey)
		b.WriteString("        ssl_session_cache shared:SSL:10m;\n")
		b.WriteString("        ssl_session_timeout 10m;\n")
		b.WriteString("        include /etc/docker-release/nginx/ssl.d/*.conf;\n\n")
	}

	b.WriteString("        location = /health {\n")
	b.WriteString("            add_header Content-Type text/plain;\n")
	b.WriteString("            return 200 \"ok\\n\";\n")
	b.WriteString("        }\n\n")

	if !ssl && cfg.RedirectHTTPS {
		b.WriteString("        location / {\n")
		b.WriteString("            return 308 https://$host$request_uri;\n")
		b.WriteString("        }\n")
	} else {
		b.WriteString("        include /shared/nginx-routes/*.location;\n")
		b.WriteString("        include /etc/docker-release/nginx/server.d/*.conf;\n")
		if ssl {
			b.WriteString("        include /etc/docker-release/nginx/https.d/*.conf;\n")
		}
	}

	b.WriteString("    }\n")
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envPort(key string, fallback int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	port, err := strconv.Atoi(v)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s must be a port 1-65535", key)
	}
	return port, nil
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func safeNginxValue(name, value string) error {
	if strings.ContainsAny(value, ";{}\n\r\t\"") {
		return fmt.Errorf("%s contains invalid nginx config characters", name)
	}
	return nil
}
