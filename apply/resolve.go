package apply

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// MissingEnvError records which *FromEnv references could not be resolved.
// Aggregated so the UI can tell admins exactly which env vars to set.
type MissingEnvError struct {
	// Refs is a list of "kind/name/field=ENV_VAR" entries so the operator can
	// fix all of them at once.
	Refs []string
}

func (e *MissingEnvError) Error() string {
	return "apply: unresolved *FromEnv references: " + strings.Join(e.Refs, ", ")
}

// IsMissingEnv reports whether err is (or wraps) a MissingEnvError.
func IsMissingEnv(err error) bool {
	var m *MissingEnvError
	return errors.As(err, &m)
}

// Resolve drains every *FromEnv field into its plaintext sibling by reading
// os.Getenv. Returns a *MissingEnvError aggregating ALL missing env vars at
// once (rather than failing on the first one), so the UI can show the full
// fix list in one shot.
func Resolve(m *Manifest) error {
	var missing []string

	for i := range m.Spec.UpstreamCredentials {
		uc := &m.Spec.UpstreamCredentials[i]
		if uc.PATFromEnv != "" {
			v, ok := os.LookupEnv(uc.PATFromEnv)
			if !ok || v == "" {
				missing = append(missing,
					fmt.Sprintf("upstream-credential/%s/pat=%s", uc.Name, uc.PATFromEnv))
				continue
			}
			uc.PAT = v
		}
	}

	if len(missing) > 0 {
		return &MissingEnvError{Refs: missing}
	}
	return nil
}
