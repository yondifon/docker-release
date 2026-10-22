//go:build !bundled_nginx && !bundled_angie

package provider

import "context"

func confirmBundledReload(_ context.Context, _ string, reload func() error) error {
	return reload()
}
