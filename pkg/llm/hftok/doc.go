// Package hftok wraps the HuggingFace tokenizers static library
// (github.com/daulet/tokenizers) in a small counting surface for the token
// estimator in pkg/llm.
//
// It loads a HuggingFace tokenizer.json or a tiktoken model file and counts the
// tokens a string encodes to. It replaces the hand-written tokenizer.json
// parser pkg/llm used to carry: the Rust library behind it is the same
// implementation HuggingFace ships, so it understands every tokenizer.json
// rather than a BPE subset.
//
// The package is a leaf: it imports only the standard library and the
// tokenizers binding, never a pkg/... package, so any layer may use it.
//
// The native archive is linked from third_party/libtokenizers/<goos>_<goarch>,
// placed there by scripts/fetch-libtokenizers.sh; link.go points cgo at it.
package hftok
