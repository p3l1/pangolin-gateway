// SPDX-License-Identifier: AGPL-3.0-only

// Package version carries build metadata stamped in via -ldflags.
package version

import "fmt"

var (
	Version = "dev"
	Commit  = "unknown"
)

func String() string {
	return fmt.Sprintf("%s (%s)", Version, Commit)
}
