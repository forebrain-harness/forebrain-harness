// Package google implements the Gemini generateContent client used by the agent
// runtime.
//
// One client serves two deployments, which differ only in the base URL: Google's
// own API (APIBase, authenticated by an API key in the Authorization header) and
// the OpenCode gateway's /google surface (authenticated by a bearer transport
// this package never sees). Nothing else in the wire format changes, so the base
// URL is the only knob.
//
// The client is deliberately non-streaming: it issues one
// POST /v1beta/models/{model}:generateContent and returns the assembled
// response. Gemini's server-sent-events variant is not implemented, so a stream
// sink observes nothing from this provider.
//
// Two content shapes are not carried, because generateContent has no equivalent
// for them:
//
//   - image parts referenced by URL: Gemini accepts inline image bytes only, so
//     such a part is refused with an error instead of being dropped silently;
//   - image parts inside a tool result: a functionResponse carries a JSON
//     object, not binary parts, so only a tool result's text reaches the model.
package google
