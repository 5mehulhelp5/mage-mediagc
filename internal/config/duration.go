package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that reads and writes Go duration strings in
// YAML, so a config file can say "30s" instead of a nanosecond count.
//
// Without this the template would have to document "timeout: 30000000000",
// which is the kind of value nobody edits correctly by hand.
type Duration time.Duration

// UnmarshalYAML parses a duration string such as "30s" or "2m".
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("timeout must be a duration string such as \"30s\": %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%q is not a duration (try \"30s\", \"2m\"): %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalYAML writes the duration back as a string.
func (d Duration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}

// Std returns the underlying time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }
