// Package subscription is the one registry of the subscriptions a user signs
// in to, and the only thing a surface knows about them.
//
// Before it existed every surface spelled the same two sign-ins out for itself:
// the gateway switched on a provider name in pkg/gateway/login_api.go, the
// terminal held its own package variables in pkg/tui/setup.go, and the web page
// carried a third copy of the flow. Adding a subscription meant writing the
// login, the probe, the model listing and the row write three more times, and
// each surface could drift from the others without anything noticing.
//
// Now a surface lists Registry() and drives every entry through Subscription
// only: probe the login this machine already holds, start a sign-in in the flow
// kind it can drive, list what the finished sign-in offers, write the chosen
// offers as model rows, and refresh a plan's key when the user rotated it. The
// web login, the terminal's /connect and the models page keep their own
// wording, progress display and routing; what they no longer own is any of the
// per-subscription knowledge.
//
// The registry holds only subscriptions with a sign-in flow (owner rule
// 2026-11-07): a provider a user connects by pasting an API key — Z.AI's Coding
// Plan, an OpenAI platform key — is an entry in the built-in provider registry
// (pkg/config), never here. Today the entries are chatgpt.go and opencode.go.
// Adding a subscription is adding one implementation and one line in
// Registry(); no surface changes at all.
//
// The package sits above pkg/process, because writing a row is the
// composition root's job (process.UpsertLLMModel) and listing a subscription's
// models is the same projection every surface displays (process.Discover*). The
// provider packages below it own the flows themselves: pkg/llm/openai and
// pkg/llm/opencode perform the OAuth exchanges and store the credentials, so a
// login made here serves every surface, and the last login wins.
package subscription
