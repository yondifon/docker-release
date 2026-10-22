package config

import "testing"

func TestNginxLabelsRejectConfigBreakingValues(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
	}{
		{"empty_host_list", map[string]string{"release.nginx.host": ",,,"}},
		{"middle_wildcard", map[string]string{"release.nginx.host": "app.*.example.com"}},
		{"both_wildcard_edges", map[string]string{"release.nginx.host": "*.example.*"}},
		{"bare_wildcard", map[string]string{"release.nginx.host": "*"}},
		{"certificate_space", map[string]string{"release.nginx.ssl.cert": "/certs/app cert.pem", "release.nginx.ssl.key": "/certs/key.pem", "release.nginx.host": "app.example.com"}},
		{"certificate_comment", map[string]string{"release.nginx.ssl.cert": "/certs/app#cert.pem", "release.nginx.ssl.key": "/certs/key.pem", "release.nginx.host": "app.example.com"}},
		{"hostless_tls", map[string]string{"release.nginx.ssl.cert": "/certs/cert.pem", "release.nginx.ssl.key": "/certs/key.pem"}},
		{"invalid_redirect", map[string]string{"release.nginx.ssl.redirect": "treu"}},
		{"reserved_health_path", map[string]string{"release.nginx.path": "/health"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.labels["release.enable"] = "true"
			tc.labels["release.provider"] = "nginx"
			if _, err := ParseLabels(tc.labels); err == nil {
				t.Fatalf("accepted labels that break nginx routing: %v", tc.labels)
			}
		})
	}
}
