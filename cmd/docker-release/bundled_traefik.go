//go:build bundled_traefik

package main

import "github.com/malico/docker-release/packaging/traefik"

const bundledProxyName = "traefik"
const bundledConfigPath = "/etc/traefik/traefik.yml"

var bundledConfig = traefik.Config
