//go:build bundled_haproxy

package main

import "github.com/malico/docker-release/packaging/haproxy"

const bundledProxyName = "haproxy"
const bundledConfigPath = "/usr/local/etc/haproxy/haproxy.cfg"

var bundledConfig = haproxy.Config
