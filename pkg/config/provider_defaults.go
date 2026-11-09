package config

// ProviderDefaultBaseURL returns the default endpoint for a provider, or ""
// for one this build does not know (those need a base_url in the config) and
// for a provider served only by a sign-in (chatgpt, opencode, opencode-go):
// their endpoint comes from the login, and a manually entered row without that
// login could not be used.
//
// The value is the first endpoint of the provider's row in provider_registry.go
// (the same list every surface offers), so a provider's default can never
// disagree with the endpoint the setup form fills in.
func ProviderDefaultBaseURL(provider string) string {
	p, ok := BuiltinProviderByID(provider)
	if !ok {
		return ""
	}
	return p.DefaultBaseURL()
}

// ProviderDefaultBaseURLTable returns the provider → default endpoint map for
// a surface that offers the whole list rather than looking one provider up.
// It is derived from the registry and is a copy: a caller may edit it without
// changing what every other surface reads.
func ProviderDefaultBaseURLTable() map[string]string {
	providers := BuiltinProviders()
	out := make(map[string]string, len(providers))
	for _, p := range providers {
		if url := p.DefaultBaseURL(); url != "" {
			out[p.ID] = url
		}
	}
	return out
}
