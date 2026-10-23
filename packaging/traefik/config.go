//go:build bundled_traefik

package traefik

import _ "embed"

//go:embed traefik.yml
var Config []byte
