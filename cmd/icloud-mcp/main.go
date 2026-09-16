// Command icloud-mcp is the iCloud MCP server: one process and all tools.
//
// Spawned by the agent through sudo as the service account, so the Apple
// app-specific password is read inside a process the agent's own uid cannot
// inspect. Stdio rather than HTTP: containment comes from the uid.
package main

import (
	"context"
	"log"
	"os"

	"github.com/mark3labs/mcp-go/server"

	"github.com/sjdonado/icloud-headless-mcp/internal/config"
	"github.com/sjdonado/icloud-headless-mcp/internal/dav"
	"github.com/sjdonado/icloud-headless-mcp/internal/drive"
	"github.com/sjdonado/icloud-headless-mcp/internal/mail"
	"github.com/sjdonado/icloud-headless-mcp/internal/mcpserver"
)

func main() {
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
