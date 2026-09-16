// Command icloud-login runs the interactive first-login session bootstrap:
// a loopback-only remote desktop onto the display the resident browser is
// on, so the owner signs into the running browser rather than a new one.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "icloud-login: not yet ported")
	os.Exit(1)
}
