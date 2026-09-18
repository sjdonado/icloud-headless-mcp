// Command icloud-mcp is the iCloud bridge as one single binary.
//
// No arguments serves the MCP server over stdio (what stdio.sh spawns).
// Every other entry point is a subcommand, so the install lays down one
// file instead of eight:
//
//	serve          the MCP server (default when no subcommand is given)
//	session-check  report whether the resident browser reaches iCloud data
//	resident       the resident headed Chromium holding the iCloud session
//	login          interactive first-login bootstrap over VNC
//	reask          re-ask Apple for web access by re-navigating, not restarting
//	drain          run the writes deferred while the grant had lapsed
//	tab-reaper     close app tabs idle longer than N minutes (default 15)
//	drive-fetch      pull the configured Drive libraries into staging
//	health-import [--self-test]
//	                 ingest the staged Apple Health export into SQLite
//	version        print the build version
//	help           print this list
//
// Spawned by the agent through sudo as the service account, so the Apple
// app-specific password is read inside a process the agent's own uid cannot
// inspect. Stdio rather than HTTP: containment comes from the uid.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/mark3labs/mcp-go/server"

	"github.com/sjdonado/icloud-headless-mcp/internal/config"
	"github.com/sjdonado/icloud-headless-mcp/internal/dav"
	"github.com/sjdonado/icloud-headless-mcp/internal/drive"
	"github.com/sjdonado/icloud-headless-mcp/internal/health"
	"github.com/sjdonado/icloud-headless-mcp/internal/mail"
	"github.com/sjdonado/icloud-headless-mcp/internal/mcpserver"
	"github.com/sjdonado/icloud-headless-mcp/internal/notes"
	"github.com/sjdonado/icloud-headless-mcp/internal/reminders"
	"github.com/sjdonado/icloud-headless-mcp/internal/session"
)

// version is stamped at release time: go build -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

const usage = `usage: icloud-mcp [subcommand] [args]

No subcommand serves the MCP server over stdio.

Subcommands:
  serve            serve the MCP server over stdio (the default)
  session-check    exit 0 healthy, 1 signed out, 2 no browser, 3 needs approval
  resident         run the resident headed Chromium holding the session
  login            interactive first-login bootstrap (VNC + 2FA)
  reask            re-ask Apple for web access without restarting the browser
  drain [--report-only]
                   run the writes deferred while the grant had lapsed
  tab-reaper [minutes]
                   close app tabs idle longer than minutes (default 15)
  drive-fetch      pull the configured Drive libraries into staging
  health-import [--self-test]
                   ingest the staged Apple Health export into SQLite
  version          print the build version
  help             print this list
`

func main() {
	os.Exit(run(os.Args[1:]))
}

// subcommands maps every documented subcommand name to its entry point, so
// deleting one breaks TestSubcommandsComplete below.
var subcommands = map[string]func([]string) int{
	"serve":         func([]string) int { runServe(); return 0 },
	"session-check": func([]string) int { return runSessionCheck() },
	"resident":      func([]string) int { return runResident() },
	"login":         func([]string) int { return runLogin() },
	"reask":         func([]string) int { return runReask() },
	"drain":         func(args []string) int { return runDrain(args) },
	"tab-reaper":    func(args []string) int { return runTabReaper(args) },
	"drive-fetch":   func([]string) int { return runDriveFetch() },
	"health-import": func(args []string) int { return runHealthImport(args) },
}

func run(args []string) int {
	if len(args) == 0 {
		args = []string{"serve"}
	}
	name, rest := args[0], args[1:]
	switch name {
	case "version":
		fmt.Println(version)
		return 0
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	}
	if fn, ok := subcommands[name]; ok {
		return fn(rest)
	}
	fmt.Fprintf(os.Stderr, "unknown subcommand %q\n%s", name, usage)
	return 2
}

func runServe() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("bad environment: %v", err)
	}
	var srv *server.MCPServer
	ask := func(ctx context.Context, question string) string {
		return mcpserver.AskApproval(ctx, srv, question)
	}
	handlers := map[string]server.ToolHandlerFunc{}
	for name, h := range dav.Handlers(cfg, ask) {
		handlers[name] = h
	}
	for name, h := range mail.Handlers(cfg, ask) {
		handlers[name] = h
	}
	for name, h := range drive.Handlers(cfg) {
		handlers[name] = h
	}
	for name, h := range notes.Handlers(cfg, ask) {
		handlers[name] = h
	}
	for name, h := range reminders.Handlers(cfg, ask) {
		handlers[name] = h
	}
	for name, h := range session.Handlers(cfg, ask) {
		handlers[name] = h
	}
	for name, h := range health.Handlers(cfg) {
		handlers[name] = h
	}
	srv = mcpserver.NewWithHandlers(handlers)
	if os.Getenv("AGENT_MCP_TRANSPORT") == "streamable-http" {
		httpServer := server.NewStreamableHTTPServer(srv)
		log.Fatal(httpServer.Start("127.0.0.1:8899"))
		return
	}
	if err := server.ServeStdio(srv); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
