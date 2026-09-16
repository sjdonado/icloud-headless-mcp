// Command icloud-reask re-asks Apple for iCloud web access without
// restarting anything: it re-navigates one app tab, which re-requests the
// grant and leaves the browser standing.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "icloud-reask: not yet ported")
	os.Exit(1)
}
