//go:build bundled_nginx

package nginx

import _ "embed"

//go:embed nginx.conf
var Config []byte
