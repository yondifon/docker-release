//go:build bundled_nginx

package main

import (
	"os"
	"strings"
)

const bundledNginxConfigPath = "/etc/nginx/nginx.conf"

type bundledNginxConfig struct {
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
	return bundledNginxConfig{}, nil
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
	b.WriteString("    include /shared/nginx-routes/*.server;\n")
	b.WriteString("    include /etc/docker-release/nginx/http.d/*.conf;\n")
	b.WriteString("    include /etc/docker-release/nginx/conf.d/*.conf;\n\n")

	renderBundledNginxDefaultServer(&b)

	b.WriteString("}\n")
	return b.String()
}

func renderBundledNginxDefaultServer(b *strings.Builder) {
	b.WriteString("    server {\n")
	b.WriteString("        listen 80 default_server;\n")
	b.WriteString("        server_name _;\n\n")

	b.WriteString("        location = /health {\n")
	b.WriteString("            add_header Content-Type text/plain;\n")
	b.WriteString("            return 200 \"ok\\n\";\n")
	b.WriteString("        }\n\n")
	b.WriteString("        include /shared/nginx-routes/*.location;\n")
	b.WriteString("        include /etc/docker-release/nginx/server.d/*.conf;\n")

	b.WriteString("    }\n")
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
