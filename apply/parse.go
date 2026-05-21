package apply

import (
	"bytes"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Parse decodes raw as YAML or JSON (auto-detected) into a Manifest, then
// validates apiVersion + kind. JSON is recognised by a leading '{' after any
// leading whitespace; everything else is parsed as YAML (YAML is a JSON
// superset for most documents but yaml.v3 is stricter about object keys, so
// we keep both code paths).
func Parse(raw []byte) (*Manifest, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("apply: empty manifest")
	}

	var m Manifest
	if looksLikeJSON(raw) {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("apply: parse JSON: %w", err)
		}
	} else {
		if err := yaml.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("apply: parse YAML: %w", err)
		}
	}

	if m.APIVersion != APIVersion {
		return nil, fmt.Errorf("apply: apiVersion must be %q, got %q", APIVersion, m.APIVersion)
	}
	if m.Kind != Kind {
		return nil, fmt.Errorf("apply: kind must be %q, got %q", Kind, m.Kind)
	}
	return &m, nil
}

func looksLikeJSON(raw []byte) bool {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == '{'
}
