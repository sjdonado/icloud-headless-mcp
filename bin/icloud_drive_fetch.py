#!/usr/bin/env python3
"""Pull the configured iCloud Drive app libraries, through the resident browser's session.

Which libraries, and where each one's files land, is `DRIVE_LIBRARIES` in this service's env file:
they are folders some app on the owner's phone writes into, so they are configuration rather than
code. See `icloud_lib/drive_libraries.py` for the shape.

This talks to what the Drive web app talks to rather than driving its UI.
`drivews.icloud.com/retrieveItemDetailsInFolders` lists a folder by `drivewsid`, and
`docws.icloud.com/ws/<zone>/download/batch` turns a file's `docwsid` into a signed URL whose bytes
are the file. Both accept the browser's own cookies. A fetch from the page's JavaScript is refused
by CORS, so the requests go through Playwright's request context, which shares the cookie jar and
is not subject to the page's origin. The one thing still taken from the live page is the query
string the app appends, `clientBuildNumber`, `clientMasteringNumber`, `clientId` and `dsid`,
captured from its first request rather than guessed.

Runs as the account that owns the browser, and writes only into its own staging directory. Moving
files into the importing services' inboxes and importing them stays with other accounts, so a
compromised browser cannot rewrite the owner's history. Files whose `etag` has not changed since
the last run are skipped; the imports are idempotent anyway, so the skip is bandwidth, not
correctness.
"""
from __future__ import annotations

import json
import re
import sys
import time
from datetime import date, timedelta
from pathlib import Path

from icloud_lib.drive_libraries import ETAGS, LIBRARIES, STAGING, pull_folder, staging_path  # noqa: E402
from icloud_lib.icloud_tabs import CDP, app_lock, blocked, quiet_hours, session_ok, _apply_timezone  # noqa: E402
from playwright.sync_api import sync_playwright  # noqa: E402

DRIVE = "https://www.icloud.com/iclouddrive/"


def _months(count: int) -> list[str]:
    """The most recent `count` months as `YYYY-MM`, newest first.

    More than one, because samples land late in the month just ended, so a run on the first of a
    month has to look back or it misses the tail of the previous one.
    """
    out, cursor = [], date.today()
    for _ in range(max(1, count)):
        out.append(cursor.strftime("%Y-%m"))
        cursor = cursor.replace(day=1) - timedelta(days=1)
    return out


class Drive:
    def __init__(self, page, hosts: dict):
        self.page = page
        self.drive_host, self.drive_q = hosts["drivews"]
        self.doc_host, self.doc_q = hosts["docws"]
        self.headers = {"Origin": "https://www.icloud.com", "Referer": "https://www.icloud.com/",
                        "Content-Type": "text/plain"}

    def _post(self, url: str, body) -> list | dict:
        r = self.page.request.post(url, data=json.dumps(body), headers=self.headers)
        if not r.ok:
            raise RuntimeError(f"HTTP {r.status} from {url.split('?')[0]}: {r.text()[:200]}")
        return r.json()

    def items(self, drivewsid: str) -> list[dict]:
        out = self._post(f"https://{self.drive_host}/retrieveItemDetailsInFolders?{self.drive_q}",
                         [{"drivewsid": drivewsid, "partialData": False, "includeHierarchy": False}])
        return out[0].get("items", []) if out else []

    def child(self, drivewsid: str, name: str) -> dict | None:
        return next((i for i in self.items(drivewsid) if i.get("name") == name), None)

    def download(self, item: dict) -> bytes:
        zone = item.get("zone") or "com.apple.CloudDocs"
        out = self._post(f"https://{self.doc_host}/ws/{zone}/download/batch?{self.doc_q}",
                         [{"document_id": item["docwsid"]}])
        url = out[0].get("data_token", {}).get("url") if out else None
        if not url:
            raise RuntimeError(f"no download url for {item.get('name')}: {json.dumps(out)[:200]}")
        r = self.page.request.get(url)
        if not r.ok:
            raise RuntimeError(f"HTTP {r.status} downloading {item.get('name')}")
        return r.body()


def _open(ctx) -> tuple:
    """Load the Drive app once and capture the hosts and query string it uses."""
    hosts: dict = {}

    def on_request(req):
        m = re.match(r"https://(p\d+-(drivews|docws)\.icloud\.com)/", req.url)
        if m and m.group(2) not in hosts:
            hosts[m.group(2)] = (m.group(1), re.sub(r".*\?", "", req.url))

    page = ctx.new_page()
    page.on("request", on_request)
    _apply_timezone(page)
    page.goto(DRIVE, timeout=120_000)
    deadline = time.time() + 90
    while time.time() < deadline and not ({"drivews", "docws"} <= hosts.keys()):
        page.wait_for_timeout(2_000)
    if not ({"drivews", "docws"} <= hosts.keys()):
        body = " ".join(f.evaluate("() => document.body ? document.body.innerText : ''")
                        for f in page.frames)
        page.close()
        raise RuntimeError("drive did not start talking to its API within 90s; page said: "
                           + re.sub(r"\s+", " ", body)[:200])
    return page, hosts


def _stage(rel: str, data: bytes) -> Path:
    target = STAGING / rel
    target.parent.mkdir(parents=True, exist_ok=True)
    tmp = target.with_suffix(target.suffix + ".part")
    tmp.write_bytes(data)
    tmp.replace(target)
    return target


def main() -> int:
    if quiet_hours():
        print("quiet hours: a fresh Drive load could raise an approval prompt nobody is awake "
              "for; skipped")
        return 3
    if not LIBRARIES:
        print("DRIVE_LIBRARIES names no library, so there is nothing to pull", file=sys.stderr)
        return 2
    etags = json.loads(ETAGS.read_text()) if ETAGS.is_file() else {}
    fetched = skipped = failed = 0
    with app_lock("Drive"), sync_playwright() as p:
        browser = p.chromium.connect_over_cdp(CDP, timeout=30_000)
        ctx = browser.contexts[0]
        if not session_ok(ctx):
            print("the iCloud browser session has expired; nothing fetched", file=sys.stderr)
            return 2
        if blocked():
            print("iCloud is waiting for a device approval; nothing fetched", file=sys.stderr)
            return 2
        page, hosts = _open(ctx)
        try:
            drive = Drive(page, hosts)
            root = drive.items("FOLDER::com.apple.CloudDocs::root")

            def pull(item: dict, rel: str) -> None:
                nonlocal fetched, skipped, failed
                key = item["docwsid"]
                if etags.get(key) == item.get("etag") and not (STAGING / rel).exists():
                    # Same content as last time, and the last copy was already handed over.
                    skipped += 1
                    print(f"  {rel}: unchanged", flush=True)
                    return
                try:
                    data = drive.download(item)
                except Exception as exc:  # noqa: BLE001 - one file failing must not stop the rest
                    failed += 1
                    print(f"  {rel}: {exc}", flush=True)
                    return
                _stage(rel, data)
                etags[key] = item.get("etag")
                fetched += 1
                print(f"  {rel}: {len(data)} bytes, modified {item.get('dateModified')}", flush=True)

            for lib in LIBRARIES:
                found_lib = next((i for i in root if i.get("name") == lib["name"]), None)
                if found_lib is None:
                    print(f"{lib['name']} not found at the Drive root", file=sys.stderr)
                    failed += 1
                    continue

                if lib["kind"] == "snapshot":
                    # One file, rewritten whole on every export. `as` carries the extension,
                    # because a library's own file name usually has none.
                    name = lib.get("file") or lib["name"]
                    f = drive.child(found_lib["drivewsid"], name)
                    if f is None:
                        print(f"{lib['name']}/{name} not found", file=sys.stderr)
                        failed += 1
                        continue
                    pull(f, lib.get("as") or staging_path(lib["dest"], name))
                    continue

                if lib["kind"] == "folder":
                    # An app export can have any layout. Keep it intact under its configured
                    # staging directory and leave interpretation to the downstream consumer.
                    pull_folder(drive, found_lib, lib["dest"], pull)
                    continue

                # A tree: per-metric folders holding monthly files named YYYY-MM.
                base = found_lib
                if lib.get("subfolder"):
                    base = drive.child(found_lib["drivewsid"], lib["subfolder"])
                    if base is None:
                        print(f"{lib['name']}/{lib['subfolder']} not found", file=sys.stderr)
                        failed += 1
                        continue
                months = _months(int(lib.get("months", 2)))
                for metric in drive.items(base["drivewsid"]):
                    if metric.get("type") != "FOLDER":
                        continue
                    files = drive.items(metric["drivewsid"])
                    for month in months:
                        f = next((x for x in files if x.get("name") == month), None)
                        if f is None:
                            continue
                        pull(f, staging_path(lib["dest"], metric["name"], f"{month}.jsonl"))
        finally:
            try:
                page.close()
            except Exception:
                pass
    ETAGS.parent.mkdir(parents=True, exist_ok=True)
    ETAGS.write_text(json.dumps(etags))
    print(f"fetched {fetched}, unchanged {skipped}, failed {failed}, into {STAGING}", flush=True)
    return 0 if fetched or skipped else 1


if __name__ == "__main__":
    sys.exit(main())
