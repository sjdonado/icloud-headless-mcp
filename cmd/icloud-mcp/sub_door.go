// Subcommand door (icloud-mcp door ADDR TTL_SECONDS PASSFILE) is the login
// door process itself. open_login and login start it; nothing else should.
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/config"
	"github.com/sjdonado/icloud-headless-mcp/internal/session"
)

func runDoor(args []string) int {
	if len(args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: icloud-mcp door HOST:PORT TTL_SECONDS PASSFILE")
		return 2
	}
	ttl, err := strconv.Atoi(args[1])
	if err != nil || ttl <= 0 {
		fmt.Fprintln(os.Stderr, "door: TTL_SECONDS must be a positive integer")
		return 2
	}
	if err := session.ServeDoor(config.LoadEnv().CDP, args[0], args[2], time.Duration(ttl)*time.Second); err != nil {
		fmt.Fprintln(os.Stderr, "door:", err)
		return 1
	}
	return 0
}
