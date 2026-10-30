// The importer's configuration writes: the runtime sections a migration
// produces go into the state database's fb_config table, the one write path
// every configuration surface shares (plan 188 W11). The migration tool is
// the user's proxy hand-edit, and gateway alone may still be written to YAML
// by it — nothing here writes that section, so nothing here writes the file.
package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// storedConfigSection reads one Root section's current value from the state
// database — the zero value when nothing is stored. The dry-run path reads
// this; a real import reads it inside its own write transaction instead.
func storedConfigSection[T any](ctx context.Context, opts *Options, section string) (T, error) {
	var zero T
	if opts == nil || opts.DB == nil {
		return zero, fmt.Errorf("the state database is required to read configuration")
	}
	store := &state.ConfigStore{DB: opts.DB}
	raw, err := store.LoadSections(ctx)
	if err != nil {
		return zero, err
	}
	doc, ok := raw[section]
	if !ok || len(doc) == 0 {
		return zero, nil
	}
	var out T
	if err := json.Unmarshal(doc, &out); err != nil {
		return zero, fmt.Errorf("read stored %s: %w", section, err)
	}
	return out, nil
}

// updateConfigSection runs one read-modify-write of a Root section in the
// state database: fn receives the section's current value — the zero value
// when nothing is stored — and the value it leaves is what lands, validated
// inside the store's transaction the way every surface's write is.
func updateConfigSection[T any](ctx context.Context, opts *Options, section string, fn func(cur *T) error) error {
	if opts == nil || opts.DB == nil {
		return fmt.Errorf("the state database is required to store configuration")
	}
	store := &state.ConfigStore{DB: opts.DB}
	return store.UpdateSection(ctx, section, func(cur json.RawMessage) (json.RawMessage, error) {
		var v T
		if len(cur) > 0 {
			if err := json.Unmarshal(cur, &v); err != nil {
				return nil, fmt.Errorf("read stored %s: %w", section, err)
			}
		}
		if err := fn(&v); err != nil {
			return nil, err
		}
		if reflect.ValueOf(&v).Elem().IsZero() {
			return nil, nil
		}
		return json.Marshal(v)
	})
}

// sectionDryRun applies fn to the section's current value without storing
// anything, so a dry run reports exactly the entries a real run would write.
func sectionDryRun[T any](ctx context.Context, opts *Options, section string, fn func(cur *T) error) error {
	cur, err := storedConfigSection[T](ctx, opts, section)
	if err != nil {
		return err
	}
	return fn(&cur)
}
