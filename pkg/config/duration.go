package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration extends time.Duration with support for human-friendly units
// such as days (d), weeks (w), months (mo) and years (y).
type Duration time.Duration

// UnmarshalYAML implements custom YAML unmarshaling for Duration.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	parsed, err := ParseHumanDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// Duration returns the underlying time.Duration value.
func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}

var durationTokenRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([a-zA-Z]+)`)

// ParseHumanDuration parses a duration string supporting standard Go units
// (ns, us, µs, ms, s, m, h) as well as extended units:
//   d/day/days   -> 24h
//   w/week/weeks -> 7d
//   mo/month/months -> 30d
//   y/year/years -> 365d
func ParseHumanDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}

	// If the string is a pure standard Go duration, use the built-in parser.
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}

	var total time.Duration
	for len(s) > 0 {
		s = strings.TrimSpace(s)
		if s == "" {
			break
		}

		m := durationTokenRe.FindStringSubmatchIndex(s)
		if m == nil {
			return 0, fmt.Errorf("invalid duration syntax near %q", s)
		}

		valStr := s[m[2]:m[3]]
		unitStr := s[m[4]:m[5]]

		val, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration value %q", valStr)
		}

		var unit time.Duration
		switch strings.ToLower(unitStr) {
		case "ns":
			unit = time.Nanosecond
		case "us", "µs":
			unit = time.Microsecond
		case "ms":
			unit = time.Millisecond
		case "s", "sec", "secs", "second", "seconds":
			unit = time.Second
		case "m", "min", "mins", "minute", "minutes":
			unit = time.Minute
		case "h", "hr", "hrs", "hour", "hours":
			unit = time.Hour
		case "d", "day", "days":
			unit = 24 * time.Hour
		case "w", "week", "weeks":
			unit = 7 * 24 * time.Hour
		case "mo", "month", "months":
			unit = 30 * 24 * time.Hour
		case "y", "year", "years":
			unit = 365 * 24 * time.Hour
		default:
			return 0, fmt.Errorf("unknown duration unit %q", unitStr)
		}

		total += time.Duration(val * float64(unit))
		s = s[m[1]:]
	}

	if total == 0 {
		return 0, fmt.Errorf("invalid duration: %q", s)
	}

	return total, nil
}
