// Package opencode implements everything forebrain needs to talk to OpenCode
// Console and the Go subscription: the OAuth 2.0 device-flow login, the
// credential file, the transport that attaches a token to every request, model
// discovery, and the static table that says which wire protocol each model
// speaks.
//
// # Two token sources
//
// A request is authenticated by exactly one of two tokens: an OAuth access
// token from the device flow (an interactive login, refreshed by Transport) or
// a Console API key from the environment (Transport built by NewKeyTransport).
// Nothing is served without one: the free models, too, need a login with a Go
// or Go Plus subscription.
//
// # The protocol split is not derivable from a model id
//
// OpenCode exposes four wire protocols behind one base URL: OpenAI
// chat/completions, OpenAI responses, Anthropic messages and Google
// generateContent. Which one a model speaks is documented by OpenCode but is
// not reported by the models endpoint, so it lives here as ProtocolFor. That
// function deliberately takes the provider as well as the model id, because two
// models (qwen3.8-max and minimax-m3) speak chat/completions on Console and
// Anthropic messages on the Go subscription.
//
// # Why the model table is in this package
//
// pkg/llm/catalog.go never overwrites an installed models.json, so adding
// opencode entries there would be invisible on every machine that already has
// one. The static metadata therefore lives in this package (like the ChatGPT
// provider's does), and the live /v1/models endpoint supplies the ids.
package opencode
