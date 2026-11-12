// Package version holds release metadata injected by the build pipeline.
package version

import "fmt"

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

func String(product string) string {
	return fmt.Sprintf("%s %s (commit %s, built %s)", product, Version, Commit, BuildTime)
}
