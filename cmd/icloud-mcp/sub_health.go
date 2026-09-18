// Subcommand health-import (icloud-mcp health-import) ingests the staged
// Apple Health export folder into SQLite. Exit 0 imported (or off with
// nothing to do), 1 failed. With --self-test it runs the six contract
// checks instead: exit 0 all pass, 1 anything failed.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/config"
	"github.com/sjdonado/icloud-headless-mcp/internal/health"
)

func runHealthImport(args []string) int {
	for _, a := range args {
		if a == "--self-test" {
			report, ok := health.SelfTest()
			fmt.Println(report)
			if !ok {
				return 1
			}
			return 0
		}
	}
	cfg := config.LoadEnv()
	if cfg.HealthExport == "" {
		fmt.Println("health extension off: HEALTH_EXPORT_DIR is empty, nothing to import")
		return 0
	}
	db, err := health.OpenRW(cfg.HealthDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open health store: %v\n", err)
		return 1
	}
	defer db.Close()
	res, err := health.ImportDir(db, cfg.HealthExport, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "import failed: %v\n", err)
		return 1
	}
	seen, fresh := 0, 0
	for _, r := range res {
		fmt.Printf("%s: %d seen, %d new\n", r.File, r.Seen, r.New)
		seen += r.Seen
		fresh += r.New
	}
	fmt.Printf("%d files, %d rows seen, %d new\n", len(res), seen, fresh)
	return 0
}
