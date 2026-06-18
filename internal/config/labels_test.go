package config

import (
	"testing"
	"time"
)

func TestParseLabels(t *testing.T) {
	labels := map[string]string{
		"release.enable":                  "true",
		"release.provider":                "nginx-proxy",
		"release.strategy":                "canary",
		"release.health_check_timeout":    "30s",
		"release.affinity":                "cookie",
		"release.bg.soak_time":            "2m",
		"release.bg.green_weight":         "60",
		"release.canary.start_percentage": "25",
		"release.canary.step":             "10",
		"release.canary.interval":         "1m",
		"release.nginx.service":           "my-nginx",
		"release.nginx.keepalive":         "20",
		"release.nginx.host":              "app.localhost,www.localhost",
		"release.nginx.path":              "/app/",
		"release.nginx.ssl.cert":          "/certs/app/fullchain.pem",
		"release.nginx.ssl.key":           "/certs/app/privkey.pem",
		"release.nginx.ssl.redirect":      "true",
	}

	cfg, err := ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.NginxService != "my-nginx" {
		t.Errorf("nginx_service = %s, want my-nginx", cfg.NginxService)
	}
	if cfg.Provider != ProviderNginxProxy {
		t.Errorf("provider = %s, want nginx-proxy", cfg.Provider)
	}
	if cfg.Strategy != StrategyCanary {
		t.Errorf("strategy = %s, want canary", cfg.Strategy)
	}
	if cfg.HealthCheckTimeout != 30*time.Second {
		t.Errorf("health_check_timeout = %v, want 30s", cfg.HealthCheckTimeout)
	}
	if cfg.Affinity != "cookie" {
		t.Errorf("affinity = %s, want cookie", cfg.Affinity)
	}
	if cfg.BlueGreen.SoakTime != 2*time.Minute {
		t.Errorf("soak_time = %v, want 2m", cfg.BlueGreen.SoakTime)
	}
	if cfg.BlueGreen.GreenWeight != 60 {
		t.Errorf("green_weight = %d, want 60", cfg.BlueGreen.GreenWeight)
	}
	if cfg.Canary.StartPercentage != 25 {
		t.Errorf("start_percentage = %d, want 25", cfg.Canary.StartPercentage)
	}
	if cfg.Canary.Step != 10 {
		t.Errorf("step = %d, want 10", cfg.Canary.Step)
	}
	if cfg.Canary.Interval != 1*time.Minute {
		t.Errorf("interval = %v, want 1m", cfg.Canary.Interval)
	}
	if cfg.NginxKeepalive != 20 {
		t.Errorf("nginx.keepalive = %d, want 20", cfg.NginxKeepalive)
	}
	if cfg.NginxPath != "/app/" {
		t.Errorf("nginx_path = %s, want /app/", cfg.NginxPath)
	}
	if cfg.NginxHost != "app.localhost,www.localhost" {
		t.Errorf("nginx_host = %s, want app.localhost,www.localhost", cfg.NginxHost)
	}
	if cfg.NginxSSLCert != "/certs/app/fullchain.pem" {
		t.Errorf("nginx_ssl_cert = %s, want /certs/app/fullchain.pem", cfg.NginxSSLCert)
	}
	if cfg.NginxSSLKey != "/certs/app/privkey.pem" {
		t.Errorf("nginx_ssl_key = %s, want /certs/app/privkey.pem", cfg.NginxSSLKey)
	}
	if !cfg.NginxSSLRedirect {
		t.Error("nginx_ssl_redirect = false, want true")
	}
}

func TestParseLabelsDefaults(t *testing.T) {
	labels := map[string]string{
		"release.enable": "true",
	}

	cfg, err := ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Provider != ProviderNginxProxy {
		t.Errorf("default provider = %s, want nginx-proxy", cfg.Provider)
	}
	if cfg.Strategy != StrategyLinear {
		t.Errorf("default strategy = %s, want linear", cfg.Strategy)
	}
	if cfg.HealthCheckTimeout != 60*time.Second {
		t.Errorf("default timeout = %v, want 60s", cfg.HealthCheckTimeout)
	}
	if cfg.Canary.StartPercentage != 10 {
		t.Errorf("default start_percentage = %d, want 10", cfg.Canary.StartPercentage)
	}
	if cfg.BlueGreen.GreenWeight != 50 {
		t.Errorf("default green_weight = %d, want 50", cfg.BlueGreen.GreenWeight)
	}
	if cfg.Affinity != "ip" {
		t.Errorf("default affinity = %s, want ip", cfg.Affinity)
	}
	if cfg.Canary.Step != 20 {
		t.Errorf("default step = %d, want 20", cfg.Canary.Step)
	}
	if cfg.NginxService != "" {
		t.Errorf("default nginx_service = %s, want empty", cfg.NginxService)
	}
	if cfg.NginxKeepalive != -1 {
		t.Errorf("default nginx_keepalive = %d, want -1", cfg.NginxKeepalive)
	}
	if cfg.NginxConfigDir != "/shared/nginx-tmpl" {
		t.Errorf("default nginx_config_dir (nginx-proxy) = %s, want /shared/nginx-tmpl", cfg.NginxConfigDir)
	}
}

func TestParseLabelsDefaultProviderFromEnv(t *testing.T) {
	t.Setenv("DR_DEFAULT_PROVIDER", "nginx")

	labels := map[string]string{
		"release.enable": "true",
	}

	cfg, err := ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Provider != ProviderNginx {
		t.Errorf("default provider = %s, want nginx", cfg.Provider)
	}
	if cfg.NginxConfigDir != "/shared/nginx-config" {
		t.Errorf("nginx_config_dir = %s, want /shared/nginx-config", cfg.NginxConfigDir)
	}
}

func TestParseLabelsExplicitProviderOverridesEnvDefault(t *testing.T) {
	t.Setenv("DR_DEFAULT_PROVIDER", "nginx")

	labels := map[string]string{
		"release.enable":   "true",
		"release.provider": "caddy",
	}

	cfg, err := ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Provider != ProviderCaddy {
		t.Errorf("provider = %s, want caddy", cfg.Provider)
	}
}

func TestProviderDefaults(t *testing.T) {
	cases := []struct {
		provider  string
		wantField func(*ServiceConfig) string
		wantValue string
	}{
		{"nginx", func(c *ServiceConfig) string { return c.NginxConfigDir }, "/shared/nginx-config"},
		{"nginx-proxy", func(c *ServiceConfig) string { return c.NginxConfigDir }, "/shared/nginx-tmpl"},
		{"angie", func(c *ServiceConfig) string { return c.AngieConfigDir }, "/shared/angie-config"},
		{"caddy", func(c *ServiceConfig) string { return c.CaddyConfigDir }, "/shared/caddy-config"},
		{"traefik", func(c *ServiceConfig) string { return c.TraefikConfigDir }, "/shared/traefik-config"},
		{"haproxy", func(c *ServiceConfig) string { return c.HAProxyConfigDir }, "/shared/haproxy-config"},
	}

	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			labels := map[string]string{
				"release.enable":   "true",
				"release.provider": tc.provider,
			}
			cfg, err := ParseLabels(labels)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := tc.wantField(cfg); got != tc.wantValue {
				t.Errorf("config_dir = %s, want %s", got, tc.wantValue)
			}
		})
	}
}

func TestParseLabelsNginxRouteDirDefault(t *testing.T) {
	labels := map[string]string{
		"release.enable":   "true",
		"release.provider": "nginx",
	}

	cfg, err := ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.NginxRouteDir != "/shared/nginx-routes" {
		t.Errorf("nginx_route_dir = %s, want /shared/nginx-routes", cfg.NginxRouteDir)
	}
}

func TestProviderDefaultsNotOverridden(t *testing.T) {
	labels := map[string]string{
		"release.enable":           "true",
		"release.provider":         "caddy",
		"release.caddy.config_dir": "/custom/caddy-config",
	}
	cfg, err := ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.CaddyConfigDir != "/custom/caddy-config" {
		t.Errorf("config_dir = %s, want /custom/caddy-config", cfg.CaddyConfigDir)
	}
}

func TestParseLabelsIgnoresNginxContainerLabel(t *testing.T) {
	labels := map[string]string{
		"release.enable":          "true",
		"release.nginx.container": "nginx",
	}

	cfg, err := ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.NginxService != "" {
		t.Errorf("nginx_service = %s, want empty", cfg.NginxService)
	}
}

func TestParseLabelsUsesNginxServiceLabel(t *testing.T) {
	labels := map[string]string{
		"release.enable":        "true",
		"release.nginx.service": "nginx",
	}

	cfg, err := ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.NginxService != "nginx" {
		t.Errorf("nginx_service = %s, want nginx", cfg.NginxService)
	}
}

func TestParseLabelsNotEnabled(t *testing.T) {
	labels := map[string]string{
		"release.enable": "false",
	}

	_, err := ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error for disabled release")
	}
}

func TestParseLabelsInvalidProvider(t *testing.T) {
	labels := map[string]string{
		"release.enable":   "true",
		"release.provider": "unknown-lb",
	}

	_, err := ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestParseLabelsCaddy(t *testing.T) {
	labels := map[string]string{
		"release.enable":           "true",
		"release.provider":         "caddy",
		"release.strategy":         "linear",
		"release.caddy.service":    "caddy",
		"release.caddy.config_dir": "/etc/caddy/conf.d",
		"release.caddy.keepalive":  "5",
	}

	cfg, err := ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Provider != ProviderCaddy {
		t.Errorf("provider = %s, want caddy", cfg.Provider)
	}
	if cfg.CaddyService != "caddy" {
		t.Errorf("caddy_service = %s, want caddy", cfg.CaddyService)
	}
	if cfg.CaddyConfigDir != "/etc/caddy/conf.d" {
		t.Errorf("caddy_config_dir = %s, want /etc/caddy/conf.d", cfg.CaddyConfigDir)
	}
	if cfg.CaddyKeepalive != 5 {
		t.Errorf("caddy_keepalive = %d, want 5", cfg.CaddyKeepalive)
	}
}

func TestParseLabelsHAProxy(t *testing.T) {
	labels := map[string]string{
		"release.enable":             "true",
		"release.provider":           "haproxy",
		"release.strategy":           "linear",
		"release.haproxy.service":    "haproxy",
		"release.haproxy.config_dir": "/etc/haproxy/conf.d",
	}

	cfg, err := ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Provider != ProviderHAProxy {
		t.Errorf("provider = %s, want haproxy", cfg.Provider)
	}
	if cfg.HAProxyService != "haproxy" {
		t.Errorf("haproxy_service = %s, want haproxy", cfg.HAProxyService)
	}
	if cfg.HAProxyConfigDir != "/etc/haproxy/conf.d" {
		t.Errorf("haproxy_config_dir = %s, want /etc/haproxy/conf.d", cfg.HAProxyConfigDir)
	}
}

func TestParseLabelsNoneCanaryRejected(t *testing.T) {
	labels := map[string]string{
		"release.enable":   "true",
		"release.provider": "none",
		"release.strategy": "canary",
	}

	_, err := ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error: provider=none with strategy=canary should be rejected")
	}
}

func TestParseLabelsNoneBlueGreenRejected(t *testing.T) {
	labels := map[string]string{
		"release.enable":   "true",
		"release.provider": "none",
		"release.strategy": "blue-green",
	}

	_, err := ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error: provider=none with strategy=blue-green should be rejected")
	}
}

func TestParseLabelsInvalidStrategy(t *testing.T) {
	labels := map[string]string{
		"release.enable":   "true",
		"release.strategy": "yolo",
	}

	_, err := ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error for unknown strategy")
	}
}

func TestParseLabelsNoneProvider(t *testing.T) {
	labels := map[string]string{
		"release.enable":   "true",
		"release.provider": "none",
		"release.strategy": "linear",
	}

	cfg, err := ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Provider != ProviderNone {
		t.Errorf("provider = %s, want none", cfg.Provider)
	}
}

func TestParseLabelsInvalidPercentage(t *testing.T) {
	labels := map[string]string{
		"release.enable":                  "true",
		"release.canary.start_percentage": "0",
	}

	_, err := ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error for percentage < 1")
	}
}

func TestParseLabelsInvalidBlueGreenWeight(t *testing.T) {
	labels := map[string]string{
		"release.enable":          "true",
		"release.bg.green_weight": "0",
	}

	_, err := ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error for blue-green weight < 1")
	}
}

func TestParseLabelsInvalidAffinity(t *testing.T) {
	labels := map[string]string{
		"release.enable":   "true",
		"release.affinity": "random",
	}

	_, err := ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error for invalid affinity")
	}
}

func TestParseLabelsConfigDirTraversal(t *testing.T) {
	labels := map[string]string{
		"release.enable":           "true",
		"release.provider":         "nginx",
		"release.nginx.config_dir": "/etc/nginx/../passwd",
	}

	_, err := ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error for config_dir with path traversal")
	}
}

func TestParseLabelsInvalidRoutePath(t *testing.T) {
	labels := map[string]string{
		"release.enable":     "true",
		"release.nginx.path": `/{return 200;}`,
	}

	_, err := ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error for invalid release.nginx.path")
	}
}

func TestParseLabelsInvalidNginxHost(t *testing.T) {
	labels := map[string]string{
		"release.enable":     "true",
		"release.nginx.host": `app.localhost;return 200`,
	}

	_, err := ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error for invalid release.nginx.host")
	}
}

func TestParseLabelsNginxSSLCertRequiresKey(t *testing.T) {
	labels := map[string]string{
		"release.enable":         "true",
		"release.nginx.ssl.cert": "/certs/app/fullchain.pem",
	}

	_, err := ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error for nginx ssl cert without key")
	}
}

func TestParseLabelsNginxSSLRedirectRequiresCert(t *testing.T) {
	labels := map[string]string{
		"release.enable":             "true",
		"release.nginx.ssl.redirect": "true",
	}

	_, err := ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error for nginx ssl redirect without cert")
	}
}

func TestParseLabelsUpstreamNameInjection(t *testing.T) {
	labels := map[string]string{
		"release.enable":   "true",
		"release.upstream": `app"; rm -rf /`,
	}

	_, err := ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error for upstream name with invalid characters")
	}
}

func TestParseLabelsValidUpstreamName(t *testing.T) {
	labels := map[string]string{
		"release.enable":   "true",
		"release.upstream": "app.example.com",
	}

	cfg, err := ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.UpstreamName != "app.example.com" {
		t.Errorf("upstream = %s, want app.example.com", cfg.UpstreamName)
	}
}

func TestParseLabelsDisabledAffinity(t *testing.T) {
	labels := map[string]string{
		"release.enable":   "true",
		"release.affinity": "",
	}

	cfg, err := ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Affinity != "" {
		t.Errorf("affinity = %q, want empty", cfg.Affinity)
	}
}
