//go:build bundled_nginx || bundled_angie

package nginx

import _ "embed"

//go:embed nginx.conf
var Config []byte
