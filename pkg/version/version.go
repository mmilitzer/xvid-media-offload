package version

import "fmt"

var (
	Version = "dev"
	Commit  = "unknown"
	BuiltAt = "unknown"
)

func String() string {
	return fmt.Sprintf("%s (%s, built %s)", Version, Commit, BuiltAt)
}
