"""The browser plumbing every iCloud surface sits on, and nothing outside this MCP uses it.

`icloud_tabs` is the CDP attach, the per-app locks, the blocked latch, quiet hours and the
timezone override. `icloud_queue` is the pending-write queue a browser-backed write lands in when
Apple's data-access grant has lapsed.

They lived in `common/` until 2026-09-10 and did not belong there. Their only callers are this
directory's own server modules and its `bin/` helpers, so `common/` was holding them for one
reader. It now holds `action_gate` alone, which three MCPs genuinely share.

Imported as `icloud_lib.icloud_tabs`, which works with no path manipulation because every module
that needs it is executed out of the same installed directory.
"""
