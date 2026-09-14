"""Which iCloud Drive libraries this install pulls, and where their files land.

An "app library" is a folder at the Drive root that some app on the owner's phone writes into.
Which ones exist is a fact about that phone, not about this server, so the list is configuration:
`DRIVE_LIBRARIES` in this service's env file, a JSON array of entries.

    [{"name": "SomeMonthlyExport", "kind": "tree",     "dest": "monthly",
      "subfolder": "raw", "months": 2},
     {"name": "SomeSnapshotExport", "kind": "snapshot", "dest": "snapshot",
      "file": "export", "as": "snapshot/export.jsonl"},
     {"name": "SomeExportingApp", "kind": "folder", "dest": "exports"}]

`kind` is the only thing the fetcher branches on:

  tree      a folder of per-metric folders holding monthly files named `YYYY-MM`. `subfolder` is
            the level under the library to walk, if any, and `months` how many recent months to
            take, newest first, because samples land late in the month just ended.
  snapshot  one file, rewritten whole on every export. `file` is its name in the library and
            `as` the path it is staged under, extension included, because the library's own name
            usually has none.
  folder    every file below the library, preserving its relative folder layout under `dest`.
            Use this for an app whose export layout is its own concern rather than a monthly tree
            or a single snapshot.

An empty or unparseable value means nothing is pulled, and the fetcher says so rather than
guessing at library names.
"""
from __future__ import annotations

import json
import os
from pathlib import Path

STATE = Path(os.environ.get("ICLOUD_STATE", Path.home()))

# Where fetched files wait for whichever account imports them, and the etag file that says what
# has already been seen. Both under this account's state, so the fetcher needs no access to any
# other service's data directory.
STAGING = Path(os.environ.get("DRIVE_STAGING", STATE / "drive-staging"))
ETAGS = Path(os.environ.get("DRIVE_ETAGS", STATE / "state" / "drive-etags.json"))


def _valid(entry: object) -> bool:
    def relative(value: object) -> bool:
        path = Path(value) if isinstance(value, str) and value else None
        return bool(path and path.parts and not path.is_absolute() and ".." not in path.parts)

    return (isinstance(entry, dict)
            and isinstance(entry.get("name"), str) and entry["name"]
            and entry.get("kind") in ("tree", "snapshot", "folder")
            and relative(entry.get("dest"))
            and ("as" not in entry or relative(entry["as"])))


def staging_path(*parts: str) -> str:
    """A safe relative staging path made from configured and Drive-provided names."""
    out: list[str] = []
    for part in parts:
        if not isinstance(part, str) or not part:
            raise ValueError(f"unsafe Drive path component: {part!r}")
        path = Path(part)
        if path.is_absolute() or any(piece in ("", ".", "..") for piece in path.parts):
            raise ValueError(f"unsafe Drive path component: {part!r}")
        out.extend(path.parts)
    return str(Path(*out))


def pull_folder(drive, folder: dict, prefix: str, pull) -> None:
    """Walk one Drive folder and hand each file to `pull` under its staging path."""
    for child in drive.items(folder["drivewsid"]):
        child_path = staging_path(prefix, child.get("name", ""))
        if child.get("type") == "FOLDER":
            pull_folder(drive, child, child_path, pull)
        else:
            pull(child, child_path)


def libraries() -> list[dict]:
    """The configured libraries, or an empty list. Never raises: a bad value pulls nothing.

    `dest` is checked for being relative and free of `..` because it is joined onto the staging
    path, and this value arrives from a file rather than from code.
    """
    try:
        parsed = json.loads(os.environ.get("DRIVE_LIBRARIES", "[]"))
    except json.JSONDecodeError:
        return []
    if not isinstance(parsed, list):
        return []
    return [e for e in parsed if _valid(e)]


LIBRARIES = libraries()
