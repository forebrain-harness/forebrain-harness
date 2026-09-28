// YAML/JSON marshalling helpers and string-list handling.
package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v2"
	yamlv3 "gopkg.in/yaml.v3"
)

// MarshalYAML renders the configuration in struct-declaration order. It walks
// Root's fields via reflection so newly added fields are always emitted. It
// deliberately writes zero-value sections too: their value-type boolean
// members can carry an explicit false, which must survive a Load/Save cycle.
//
// A section marked omitempty that carries nothing at all is skipped rather than
// written as `key: {}`. An empty mapping documents neither the setting nor its
// default, and Save materializes the defaults of every optional setting first,
// so a section that still renders empty genuinely has nothing to say.
func (r Root) MarshalYAML() (any, error) {
	v := reflect.ValueOf(r)
	t := v.Type()
	out := make(yaml.MapSlice, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		name, omitempty := yamlFieldName(field)
		if name == "-" {
			continue
		}
		fv := v.Field(i)
		if omitempty && rendersEmpty(fv.Interface()) {
			continue
		}
		out = append(out, yaml.MapItem{Key: name, Value: fv.Interface()})
	}
	return out, nil
}

// rendersEmpty reports whether value carries no configuration once encoded.
// It asks the encoder instead of inspecting the type so custom marshalers and
// omitempty on nested fields are accounted for.
func rendersEmpty(value any) bool {
	encoded, err := yaml.Marshal(value)
	if err != nil {
		return false
	}
	switch strings.TrimSpace(string(encoded)) {
	case "", "{}", "[]", "null", `""`:
		return true
	default:
		return false
	}
}

// yamlFieldName returns the yaml key for a struct field and whether it carries
// the omitempty option. A missing tag falls back to the lowercased field name,
// matching go.yaml.in/yaml/v2's default.
func yamlFieldName(field reflect.StructField) (string, bool) {
	tag := field.Tag.Get("yaml")
	if tag == "" {
		return strings.ToLower(field.Name), false
	}
	parts := strings.Split(tag, ",")
	name := parts[0]
	if name == "" {
		name = strings.ToLower(field.Name)
	}
	omitempty := false
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			omitempty = true
		}
	}
	return name, omitempty
}

type StringList []string

func (s *StringList) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var single string
	if err := unmarshal(&single); err == nil {
		*s = normalizeModelStringList([]string{single})
		return nil
	}
	var many []string
	if err := unmarshal(&many); err != nil {
		return err
	}
	*s = normalizeModelStringList(many)
	return nil
}

func (s *StringList) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*s = normalizeModelStringList([]string{single})
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return err
	}
	*s = normalizeModelStringList(many)
	return nil
}

func (s *StringList) UnmarshalYAMLNode(n *yamlv3.Node) error {
	if n == nil {
		*s = nil
		return nil
	}
	switch n.Kind {
	case yamlv3.ScalarNode:
		var single string
		if err := n.Decode(&single); err != nil {
			return err
		}
		*s = normalizeModelStringList([]string{single})
		return nil
	case yamlv3.SequenceNode:
		many := make([]string, 0, len(n.Content))
		for _, child := range n.Content {
			var item string
			if err := child.Decode(&item); err != nil {
				return err
			}
			many = append(many, item)
		}
		*s = normalizeModelStringList(many)
		return nil
	default:
		return fmt.Errorf("model must be string or string array")
	}
}

func (s StringList) first() string {
	return s.First()
}

func (s StringList) First() string {
	if len(s) == 0 {
		return ""
	}
	return strings.TrimSpace(s[0])
}

func normalizeModelStringList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
