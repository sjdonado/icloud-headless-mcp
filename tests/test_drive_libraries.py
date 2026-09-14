import unittest

from icloud_lib.drive_libraries import _valid, pull_folder, staging_path


class DriveLibrariesTest(unittest.TestCase):
    def test_folder_library_is_valid_and_staging_dest_stays_relative(self):
        self.assertTrue(_valid({"name": "Example Exports", "kind": "folder", "dest": "exports"}))
        self.assertFalse(_valid({"name": "Example Exports", "kind": "folder", "dest": "../exports"}))
        self.assertFalse(_valid({"name": "Example Exports", "kind": "folder", "dest": "."}))
        self.assertFalse(_valid({"name": "Example Export", "kind": "snapshot", "dest": "exports", "as": "/tmp/export"}))

    def test_staging_path_rejects_parent_directory(self):
        self.assertEqual(staging_path("exports", "daily", "report.csv"), "exports/daily/report.csv")
        with self.assertRaises(ValueError):
            staging_path("exports", "../report.csv")

    def test_folder_walk_preserves_nested_paths(self):
        class Drive:
            items_by_id = {
                "root": [{"name": "reports", "type": "FOLDER", "drivewsid": "reports"},
                         {"name": "index.json", "docwsid": "index"}],
                "reports": [{"name": "2026", "type": "FOLDER", "drivewsid": "2026"}],
                "2026": [{"name": "summary.csv", "docwsid": "summary"}],
            }

            def items(self, drivewsid):
                return self.items_by_id[drivewsid]

        pulled = []
        pull_folder(Drive(), {"drivewsid": "root"}, "example-app",
                    lambda item, path: pulled.append((item["docwsid"], path)))
        self.assertEqual(pulled, [("summary", "example-app/reports/2026/summary.csv"),
                                  ("index", "example-app/index.json")])


if __name__ == "__main__":
    unittest.main()
