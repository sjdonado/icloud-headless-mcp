// Command icloud-resident is the resident headed Chromium holding the
// iCloud session. It loads only icloud.com at startup and opens no app tab,
// so an automatic restart never raises a data-access prompt by itself.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "icloud-resident: not yet ported")
	os.Exit(1)
}
