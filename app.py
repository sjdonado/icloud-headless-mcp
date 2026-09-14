#!/usr/bin/env python3
"""The one server object every iCloud tool module registers on, and the account it speaks for.

Headless, and the name says so. Calendar, contacts and mail go over DAV and IMAP, which need
nothing but the credentials; Notes and Reminders have no protocol at all and are driven through a
resident headless browser holding a live web session. That second half is why a call here can
take ninety seconds and why a lapsed web-access grant is a failure mode this server has to name
rather than hide.

One process and one server entry, so a Notes tool is `<prefix>_create_note` rather than living
behind a second server.

**One process is not one lock.** The browser-backed modules keep their own per-app locks, so a
Notes call holding the browser cannot serialise a mail read behind it: DAV and IMAP need no lock
at all and take none. That is the property worth testing after any change here, and the test is
literally `list_mail` while a Notes call holds its lock.

**And one process is one approval mechanism.** Every write that needs the owner's word asks
through MCP elicitation, over the protocol, with `ask_approval` below. This server holds no
messaging credential and sends nothing on its own: anything the owner has to know is in the tool
result for the client to relay.
"""
import os
from datetime import date, datetime
from zoneinfo import ZoneInfo

from mcp.server.mcpserver import Context
from mcp.server.mcpserver import MCPServer
from mcp.shared.exceptions import MCPError
from pydantic import BaseModel

USER = os.environ["ICLOUD_APPLE_ID"]
PW = os.environ["ICLOUD_APP_PASSWORD"]
# Required, and no city as a default: a zone guessed here types the wrong hour into an
# Apple picker and nothing errors. `AGENT_TZ_FILE` overrides it when the owner travels.
TZ = ZoneInfo(os.environ["AGENT_TZ"])
DEFAULT_LIST = os.environ.get("AGENT_DEFAULT_LIST", "Today")

mcp = MCPServer("icloud-headless-mcp")


class Approve(BaseModel):
    """One optional boolean, and the default is what makes a plain tap work.

    A client whose approval surface is a yes/no tap answers with
    `ElicitResult(action="accept", content={})`, an empty object. The SDK validates that content
    against this schema before handing it back, so a *required* field rejects a genuine approval:
    a `confirm: bool` here turned two taps into "content does not match the requested schema" and
    left the event undeleted. A field with a default is not required, so the empty answer
    validates as `approve=True`, while a client that does render the field can still say no.
    """

    approve: bool = True


async def ask_approval(ctx: Context, question: str) -> str | None:
    """Ask the owner. Returns None when approved, or the reason to refuse.

    Fails closed three ways over, because they are three different shapes of "nobody is there".
    A session with somebody in it declines. A client that never declared the elicitation
    capability answers the request with an error. And a transport with no back-channel at all
    raises before the request is sent. The last two are what a scheduled run looks like from in
    here, and `MCPError` is the base of both, so asking and failing to ask both end the same way:
    nothing executed, and a result that says why.
    """
    try:
        answer = await ctx.elicit(message=question, schema=Approve)
    except MCPError as exc:
        return (f"nobody was present to approve it, so nothing was done: this session cannot "
                f"ask ({exc}). Ask again in a conversation where the owner can answer")
    if answer.action != "accept":
        return f"not approved ({answer.action})"
    if getattr(answer.data, "approve", True) is not True:
        return "not approved (the answer was no)"
    return None


def _day(value: str, field: str) -> date:
    """A `YYYY-MM-DD` string, or a clear error naming which argument was wrong."""
    try:
        return datetime.strptime(value, "%Y-%m-%d").date()
    except ValueError as exc:
        raise ValueError(f"{field} must be YYYY-MM-DD, got {value!r}: {exc}") from exc
