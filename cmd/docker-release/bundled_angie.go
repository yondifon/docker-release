//go:build bundled_angie

package main

import angie "github.com/malico/docker-release/packaging/angie"

const bundledProxyName = "angie"
const bundledConfigPath = "/etc/angie/angie.conf"

var bundledConfig = angie.Config
