//go:build bundled_nginx

package main

import nginx "github.com/malico/docker-release/packaging/nginx"

const bundledProxyName = "nginx"
const bundledConfigPath = "/etc/nginx/nginx.conf"

var bundledConfig = nginx.Config
