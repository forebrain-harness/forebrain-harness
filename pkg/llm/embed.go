package llm

import _ "embed"

//go:embed model_assets/models.json
var embeddedModelsJSON []byte

// EmbeddedModelsJSON returns the build-time embedded models.json catalog
// bytes. The same content is seeded to $FOREBRAIN_HOME/models.json by Install on
// first run; callers that need the catalog without a home directory (e.g.
// tests) can parse these bytes directly.
func EmbeddedModelsJSON() []byte {
	return embeddedModelsJSON
}
