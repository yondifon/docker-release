package config

import "testing"

func TestParseLabelsRejectsUnsafeConfigurations(t *testing.T) {
	cases := []map[string]string{
		{"release.enable": "false"},
		{"release.provider": "unknown-lb"},
		{"release.strategy": "yolo"},
		{"release.provider": "none", "release.strategy": "canary"},
		{"release.provider": "none", "release.strategy": "blue-green"},
		{"release.canary.start_percentage": "0"},
		{"release.bg.green_weight": "0"},
		{"release.affinity": "random"},
		{"release.nginx.config_dir": "/etc/nginx/../passwd"},
		{"release.nginx.path": `/{return 200;}`},
		{"release.nginx.host": `app.localhost;return 200`},
		{"release.nginx.ssl.cert": "/certs/app/fullchain.pem"},
		{"release.nginx.ssl.redirect": "true"},
		{"release.upstream": `app"; rm -rf /`},
	}
	for _, labels := range cases {
		if _, exists := labels["release.enable"]; !exists {
			labels["release.enable"] = "true"
		}
		if _, err := ParseLabels(labels); err == nil {
			t.Errorf("accepted unsafe labels: %v", labels)
		}
	}
}

func TestExplicitProviderDoesNotInheritBundledNginxDefaults(t *testing.T) {
	t.Setenv("DR_DEFAULT_PROVIDER", "nginx")
	cfg, err := ParseLabels(map[string]string{"release.enable": "true", "release.provider": "caddy"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != ProviderCaddy || cfg.NginxConfigDir != "" || cfg.CaddyConfigDir == "" {
		t.Fatalf("bundled nginx defaults leaked into a different provider: %+v", cfg)
	}
}
