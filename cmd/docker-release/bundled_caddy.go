//go:build bundled_caddy

package main

import "github.com/malico/docker-release/packaging/caddy"

const bundledProxyName = "caddy"
const bundledConfigPath = "/etc/caddy/Caddyfile"

var bundledConfig = caddy.Config
