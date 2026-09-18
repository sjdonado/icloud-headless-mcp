// Subcommand reask (icloud-mcp reask) re-asks Apple for web access by
// re-navigating the Reminders tab, not by restarting the browser: a restart
// discards warm tabs and raises an approval prompt of its own, while a
// navigation re-requests the grant and leaves everything standing. One app,
// not two: the grant covers iCloud.com data, so one navigation raises one
// prompt.
package main

import (
	"fmt"

	"github.com/sjdonado/icloud-headless-mcp/internal/config"
	"github.com/sjdonado/icloud-headless-mcp/internal/session"
)

func runReask() int {
	out, code := session.Reask(config.LoadEnv())
	fmt.Println(out)
	return code
}
