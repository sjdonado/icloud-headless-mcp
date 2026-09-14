#!/usr/bin/env python3
"""The iCloud MCP server: one process and five tool modules.

Importing a tool module is what registers its tools on the shared server object, so this file is
the list of surfaces and nothing else. Adding a surface is a module and a line here.

Spawned by the agent through sudo as agent-icloud, so the Apple app-specific password is read
inside a process the agent's own uid cannot inspect. stdio rather than HTTP: containment comes
from the uid, and a loopback MCP URL was refused by the runtime that preceded this one anyway.
"""
import os

from app import mcp

# noqa: F401 on every one of these. The import *is* the registration; nothing calls them.
from tools import dav        # noqa: F401  calendar and contacts over CalDAV
from tools import mail       # noqa: F401  mail over IMAP, and the confirmed send
from tools import notes      # noqa: F401  Notes, through the resident browser
from tools import reminders  # noqa: F401  Reminders, through the resident browser
from tools import drive      # noqa: F401  what the daily Drive pull last fetched

if __name__ == "__main__":
    transport = os.environ.get("AGENT_MCP_TRANSPORT", "stdio")
    if transport == "streamable-http":
        mcp.run(transport="streamable-http", host="127.0.0.1", port=8899)
    else:
        mcp.run(transport="stdio")
