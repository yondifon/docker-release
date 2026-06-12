package provider

import "context"

type NoopProvider struct{}

func NewNoop() *NoopProvider {
	return &NoopProvider{}
}

func (p *NoopProvider) GenerateConfig(_ context.Context, _ *UpstreamState) error {
	return nil
}

func (p *NoopProvider) Reload(_ context.Context) error {
	return nil
}
