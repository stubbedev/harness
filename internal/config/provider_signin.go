package config

import (
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/oauth"
	"github.com/stubbedev/harness/internal/oauth/copilot"
	"github.com/stubbedev/harness/internal/oauth/opencode"
)

// SignInFlow returns the device authorization grant a provider is
// connected with, for the providers that take a sign-in instead of an
// API key. Both OpenCode gateways share the OpenCode Console account.
func SignInFlow(id catalog.InferenceProvider) (oauth.DeviceFlow, bool) {
	switch id {
	case catalog.InferenceProviderCopilot:
		return copilot.DeviceFlow{}, true
	case catalog.InferenceProviderOpenCodeGo, catalog.InferenceProviderOpenCodeZen:
		return opencode.DeviceFlow{Provider: string(id)}, true
	default:
		return nil, false
	}
}
