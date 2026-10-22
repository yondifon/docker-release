//go:build bundled_angie

package angie

import (
	"strings"

	"github.com/malico/docker-release/packaging/nginx"
)

var Config = []byte(strings.ReplaceAll(string(nginx.Config), "nginx", "angie"))
