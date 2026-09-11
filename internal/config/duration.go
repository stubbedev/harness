package config

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/invopop/jsonschema"
)

// Duration is a time.Duration that reads the way people write durations in a
// config file: "30s", "2m", "1h30m". A bare number is accepted too and taken
// as nanoseconds, which is what encoding/json does with a plain
// time.Duration.
type Duration time.Duration

// Duration returns the value as a [time.Duration].
func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}

// String implements [fmt.Stringer].
func (d Duration) String() string {
	return time.Duration(d).String()
}

// UnmarshalJSON implements [json.Unmarshaler].
func (d *Duration) UnmarshalJSON(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	switch value := v.(type) {
	case string:
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", value, err)
		}
		*d = Duration(parsed)
		return nil
	case float64:
		*d = Duration(time.Duration(value))
		return nil
	default:
		return fmt.Errorf("invalid duration %v: want a string like \"30s\" or a number of nanoseconds", v)
	}
}

// MarshalJSON implements [json.Marshaler]. Durations are written back in the
// string form so a value Harness persists reads the same as one a user typed.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// JSONSchema describes Duration for the generated schema: editors should
// suggest a duration string, while a raw nanosecond count still validates.
func (Duration) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		OneOf: []*jsonschema.Schema{
			{
				Type:        "string",
				Pattern:     `^-?(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$`,
				Description: "Duration string, e.g. 30s, 2m, 1h30m",
			},
			{
				Type:        "integer",
				Description: "Duration in nanoseconds",
			},
		},
	}
}
