// Command icloud-drive-fetch pulls the configured Drive libraries and
// stages files for another account to import. It fetches, stages, and
// reports; it never reads a staged file back and never hands one on.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "icloud-drive-fetch: not yet ported")
	os.Exit(1)
}
