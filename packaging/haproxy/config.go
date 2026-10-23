//go:build bundled_haproxy

package haproxy

import _ "embed"

//go:embed haproxy.cfg
var Config []byte
