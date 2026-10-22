//go:build !bundled_nginx

package provider

import "context"

func confirmBundledNginxReload(_ context.Context, reload func() error) error {
	return reload()
}
