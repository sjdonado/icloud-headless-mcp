import os
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

os.environ.setdefault("ICLOUD_APPLE_ID", "test@example.com")
os.environ.setdefault("ICLOUD_APP_PASSWORD", "test")
os.environ.setdefault("AGENT_TZ", "Europe/Amsterdam")

try:
    from tools import dav
except ImportError:
    # caldav, vobject, and mcp are install-time dependencies, absent from a
    # bare checkout. The suite stays green without them; the venv run below
    # exercises the real path.
    dav = None

ADA = ("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Ada Lovelace\r\n"
       "EMAIL:ada@example.com\r\nTEL:+3101234567\r\nEND:VCARD\r\n")
GRACE = ("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Grace Hopper\r\n"
         "EMAIL:grace@navy.mil\r\nEND:VCARD\r\n")
BROKEN = "BEGIN:VCARD\r\nFN:No End In Sight\r\n"


def _xml(body):
    return ("<?xml version=\"1.0\" encoding=\"utf-8\"?>"
            "<D:multistatus xmlns:D=\"DAV:\" "
            "xmlns:C=\"urn:ietf:params:xml:ns:caldav\" "
            "xmlns:A=\"urn:ietf:params:xml:ns:carddav\">" +
            body + "</D:multistatus>").encode()


def _esc(text):
    return (text.replace("&", "&amp;").replace("<", "&lt;")
            .replace(">", "&gt;"))


class FakeCardDAV(BaseHTTPRequestHandler):
    def _send_xml(self, body):
        data = _xml(body)
        self.send_response(207)
        self.send_header("Content-Type", "application/xml")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _read_body(self):
        length = int(self.headers.get("Content-Length", 0))
        return self.rfile.read(length) if length else b""

    def do_PROPFIND(self):
        self._read_body()
        if self.path == "/":
            self._send_xml(
                "<D:response><D:href>/</D:href><D:propstat><D:prop>"
                "<D:current-user-principal><D:href>/principal/</D:href>"
                "</D:current-user-principal>"
                "</D:prop><D:status>HTTP/1.1 200 OK</D:status>"
                "</D:propstat></D:response>")
        elif self.path == "/principal/":
            self._send_xml(
                "<D:response><D:href>/principal/</D:href>"
                "<D:propstat><D:prop>"
                "<A:addressbook-home-set><D:href>/books/</D:href>"
                "</A:addressbook-home-set>"
                "</D:prop><D:status>HTTP/1.1 200 OK</D:status>"
                "</D:propstat></D:response>")
        elif self.path == "/books/":
            self._send_xml(
                "<D:response><D:href>/books/Home/</D:href>"
                "<D:propstat><D:prop>"
                "<D:resourcetype><D:collection/><A:addressbook/>"
                "</D:resourcetype><D:displayname>Home</D:displayname>"
                "</D:prop><D:status>HTTP/1.1 200 OK</D:status>"
                "</D:propstat></D:response>"
                "<D:response><D:href>/books/Other/</D:href>"
                "<D:propstat><D:prop>"
                "<D:resourcetype><D:collection/></D:resourcetype>"
                "<D:displayname>Not a book</D:displayname>"
                "</D:prop><D:status>HTTP/1.1 200 OK</D:status>"
                "</D:propstat></D:response>")
        else:
            self.send_error(404)

    def do_REPORT(self):
        self._read_body()
        if self.path != "/books/Home/":
            self.send_error(404)
            return
        cards = "".join(
            "<D:response><D:href>/books/Home/%d.vcf</D:href>"
            "<D:propstat><D:prop><A:address-data>%s</A:address-data>"
            "</D:prop><D:status>HTTP/1.1 200 OK</D:status>"
            "</D:propstat></D:response>" % (i, _esc(card))
            for i, card in enumerate([ADA, GRACE, BROKEN]))
        self._send_xml(cards)

    def log_message(self, *args):
        pass


@unittest.skipUnless(dav is not None, "requires caldav, vobject, mcp")
class SearchContactsTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = ThreadingHTTPServer(("127.0.0.1", 0), FakeCardDAV)
        cls.thread = threading.Thread(target=cls.server.serve_forever,
                                      daemon=True)
        cls.thread.start()
        cls._base = dav._CARDDAV_BASE
        host, port = cls.server.server_address
        dav._CARDDAV_BASE = f"http://{host}:{port}"

    @classmethod
    def tearDownClass(cls):
        dav._CARDDAV_BASE = cls._base
        cls.server.shutdown()
        cls.thread.join()

    def test_match_by_name(self):
        out = dav.search_contacts("ada")
        self.assertEqual(out["count"], 1)
        self.assertEqual(out["contacts"][0]["name"], "Ada Lovelace")

    def test_match_case_insensitive_email(self):
        out = dav.search_contacts("NAVY")
        self.assertEqual([c["name"] for c in out["contacts"]],
                         ["Grace Hopper"])

    def test_match_by_phone(self):
        out = dav.search_contacts("+3101")
        self.assertEqual([c["name"] for c in out["contacts"]],
                         ["Ada Lovelace"])

    def test_broken_card_skipped(self):
        out = dav.search_contacts("sight")
        self.assertEqual(out["count"], 0)

    def test_empty_query(self):
        self.assertEqual(dav.search_contacts("   "),
                         {"error": "empty query"})


if __name__ == "__main__":
    unittest.main()
