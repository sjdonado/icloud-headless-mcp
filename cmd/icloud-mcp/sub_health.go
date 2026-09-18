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
	// Report first, fail after: files commit one transaction at a time,
	// so a failing run leaves committed work behind, and "import failed"
	// alone reads as "nothing happened". The operator re-running after a
	// fix needs to know the state the store is already in.
	seen, fresh, refreshed := 0, 0, 0
	for _, r := range res {
		fmt.Printf("%s: %d seen, %d new, %d updated\n", r.File, r.Seen, r.New, r.Updated)
		seen += r.Seen
		fresh += r.New
		refreshed += r.Updated
	}
	fmt.Printf("%d files, %d rows seen, %d new, %d updated\n", len(res), seen, fresh, refreshed)
	fmt.Printf("store: %s\n", cfg.HealthDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "import failed: %v\n", err)
		return 1
	}
	return 0
}
