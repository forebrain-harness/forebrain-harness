package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v2"
)

// LegacyChannelsMigration reports what MigrateLegacyAgentChannels changed.
type LegacyChannelsMigration struct {
	// Removed is true when a primary-agent-level channels section was found and
	// stripped from the file.
	Removed bool
	// Enabled names the channels that were enabled when they were removed, so
	// the caller can warn that they stopped being reachable.
	Enabled []string
	// Backup is the file the pre-migration bytes were copied to, empty when
	// nothing was removed.
	Backup string
}

// legacyChannelsDoc is the shape the migration reads: just the channels
// sections under agents.definitions, and only their enabled flags.
type legacyChannelsDoc struct {
	Agents struct {
		Definitions map[string]struct {
			Channels map[string]struct {
				Enabled bool `yaml:"enabled"`
			} `yaml:"channels"`
		} `yaml:"definitions"`
	} `yaml:"agents"`
}

// MigrateLegacyAgentChannels retires the primary-agent-level channels sections
// plan 035 replaced with per-project channels (owner 2026-10-11). When the
// file still carries agents.definitions.<id>.channels it copies the file aside
// to <path>.bak-<UTC> and rewrites it without that section, using the engine's
// own write path. A file with no channels section is left byte for byte as it
// is.
func MigrateLegacyAgentChannels(path string) (LegacyChannelsMigration, error) {
	var out LegacyChannelsMigration
	p := strings.TrimSpace(path)
	if p == "" {
		return out, nil
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	var doc legacyChannelsDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return out, fmt.Errorf("read legacy channels: %w", err)
	}
	found := false
	var enabled []string
	for _, def := range doc.Agents.Definitions {
		if len(def.Channels) == 0 {
			continue
		}
		found = true
		for name, ch := range def.Channels {
			if ch.Enabled {
				enabled = append(enabled, strings.TrimSpace(name))
			}
		}
	}
	if !found {
		return out, nil
	}
	sort.Strings(enabled)
	backup := p + ".bak-" + time.Now().UTC().Format("20060102T150405Z")
	if err := os.WriteFile(backup, raw, 0o600); err != nil {
		return out, fmt.Errorf("back up %s: %w", p, err)
	}
	root, err := LoadPersisted(p)
	if err != nil {
		return out, fmt.Errorf("load config to strip channels: %w", err)
	}
	if err := Save(p, root); err != nil {
		return out, fmt.Errorf("rewrite config without channels: %w", err)
	}
	out.Removed = true
	out.Enabled = enabled
	out.Backup = backup
	return out, nil
}
