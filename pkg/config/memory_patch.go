package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v2"
)

type MemoryPatch struct {
	FeatureEnabled   *bool
	UseMemories      *bool
	GenerateMemories *bool
}

func PatchMemory(path string, patch MemoryPatch) error {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(path)))
	switch ext {
	case ".json":
		return patchMemoryJSON(path, patch)
	default:
		return patchMemoryYAML(path, patch)
	}
}

func patchMemoryYAML(path string, patch MemoryPatch) error {
	doc, err := loadPatchYAML(path)
	if err != nil {
		return err
	}
	features := ensurePatchSectionYAML(doc, "features")
	memories := ensurePatchSectionYAML(doc, "memories")
	applyMemoryPatchMap(func(section, key string, value bool) {
		if section == "features" {
			features[key] = value
		} else {
			memories[key] = value
		}
	}, patch)
	return writePatchYAML(path, doc)
}

func patchMemoryJSON(path string, patch MemoryPatch) error {
	doc, err := loadPatchJSON(path)
	if err != nil {
		return err
	}
	features := ensurePatchSectionJSON(doc, "features")
	memories := ensurePatchSectionJSON(doc, "memories")
	applyMemoryPatchMap(func(section, key string, value bool) {
		if section == "features" {
			features[key] = value
		} else {
			memories[key] = value
		}
	}, patch)
	return writePatchJSON(path, doc)
}

func applyMemoryPatchMap(set func(string, string, bool), patch MemoryPatch) {
	if patch.FeatureEnabled != nil {
		set("features", "memories", *patch.FeatureEnabled)
	}
	if patch.UseMemories != nil {
		set("memories", "use_memories", *patch.UseMemories)
	}
	if patch.GenerateMemories != nil {
		set("memories", "generate_memories", *patch.GenerateMemories)
	}
}

func loadPatchYAML(path string) (map[interface{}]interface{}, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc := map[interface{}]interface{}{}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
	}
	if doc == nil {
		doc = map[interface{}]interface{}{}
	}
	return doc, nil
}

func writePatchYAML(path string, doc map[interface{}]interface{}) error {
	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}

func ensurePatchSectionYAML(doc map[interface{}]interface{}, key string) map[interface{}]interface{} {
	section, _ := doc[key].(map[interface{}]interface{})
	if section == nil {
		section = map[interface{}]interface{}{}
		doc[key] = section
	}
	return section
}

func loadPatchJSON(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}

func writePatchJSON(path string, doc map[string]any) error {
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}

func ensurePatchSectionJSON(doc map[string]any, key string) map[string]any {
	section, _ := doc[key].(map[string]any)
	if section == nil {
		section = map[string]any{}
		doc[key] = section
	}
	return section
}
