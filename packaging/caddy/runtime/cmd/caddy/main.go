package main

import (
	caddycmd "github.com/caddyserver/caddy/v2/cmd"
	_ "github.com/caddyserver/caddy/v2/modules/standard"
	_ "github.com/malico/docker-release/packaging/caddy/runtime"
)

func main() {
	caddycmd.Main()
}
