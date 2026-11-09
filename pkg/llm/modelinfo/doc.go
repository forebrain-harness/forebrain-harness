// Package modelinfo answers, live, the reasoning levels and capability data a
// configured model offers, from the source that owns that model: the ChatGPT
// subscription's backend for its own rows, Volcengine Ark's plan data plane for
// its own, and models.dev for everything else (owner 2026-11-07, see
// docs/plan/SUBSCRIPTION_EFFORT_SOURCES.md).
//
// It is a leaf of the llm layer: it imports the provider packages it calls and
// never pkg/config, because a configured row lives above it. pkg/process
// projects a row into Row and drives the resolve — off the request path, with
// every source's own timeout, and with a silent fall-back that leaves the
// surface showing the "pick the levels yourself" boxes rather than an error.
package modelinfo
