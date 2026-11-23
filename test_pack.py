# language: Python, file: test_pack.py, runtime: Python 3.10+, target: Unit tests for pack.py
import json
import os
import shutil
import tempfile
import unittest
from pathlib import Path

from pack import (
    is_existing_digest,
    is_binary_file,
    decode_text,
    clean_notebook_content,
    discover_project_files,
    pack_project,
    parse_args,
    GLOBAL_EXCLUDE_DIRS,
    EXCLUDE_EXTENSIONS,
    EXCLUDE_FILES,
    ALLOWED_DOTFILES
)


class TestPackDigest(unittest.TestCase):
    def setUp(self):
        self.test_dir = tempfile.mkdtemp(prefix="unicompute_pack_test_")
        self.root = Path(self.test_dir)

    def tearDown(self):
        shutil.rmtree(self.test_dir, ignore_errors=True)

    def test_is_existing_digest_variants(self):
        # 1. Standard Digest with LF
        digest_lf = b"Directory structure:\n  pkg/blobstore.go\n\n================================================\nFILE: pkg/blobstore.go\n================================================\ncontent\n"
        self.assertTrue(is_existing_digest(digest_lf))

        # 2. Standard Digest with CRLF
        digest_crlf = b"Directory structure:\r\n  pkg/blobstore.go\r\n\r\n================================================\r\nFILE: pkg/blobstore.go\r\n================================================\r\ncontent\r\n"
        self.assertTrue(is_existing_digest(digest_crlf))

        # 3. Digest with UTF-8 BOM (very common on Windows)
        digest_bom = b"\xef\xbb\xbfDirectory structure:\n  pkg/blobstore.go\n\n================================================\nFILE: pkg/blobstore.go\n================================================\ncontent\n"
        self.assertTrue(is_existing_digest(digest_bom))

        # 4. Large Digest with > 4096 bytes of file list before first FILE marker (e.g. 150 files)
        lines = ["Directory structure:"] + [f"  pkg/module_{i}/sub/handler_file.go" for i in range(160)]
        lines.append("\n================================================\nFILE: pkg/module_0/sub/handler_file.go\n================================================\npackage sub\n")
        large_digest = "\n".join(lines).encode("utf-8")
        self.assertGreater(len(large_digest), 4096)
        self.assertTrue(is_existing_digest(large_digest))
        self.assertTrue(is_existing_digest(b"\xef\xbb\xbf" + large_digest))

        # 5. Normal text file mentioning Directory structure in comment/body
        normal_code = b"// Some header\nDirectory structure:\nFILE: test\n======"
        self.assertFalse(is_existing_digest(normal_code))

        # 6. Empty data
        self.assertFalse(is_existing_digest(b""))

        # 7. Random python file
        py_code = b"import os\nprint('hello')\n"
        self.assertFalse(is_existing_digest(py_code))

    def test_is_binary_file(self):
        # Text file with ASCII
        txt_path = self.root / "file.txt"
        txt_path.write_text("Hello World", encoding="utf-8")
        self.assertFalse(is_binary_file(txt_path))

        # UTF-16 LE text file (has null bytes in ASCII range, but valid text)
        utf16_path = self.root / "script.ps1"
        utf16_path.write_bytes('Write-Host "Hello"'.encode("utf-16"))
        self.assertFalse(is_binary_file(utf16_path))

        # Real binary file (contains null bytes without UTF-16 BOM)
        bin_path = self.root / "program.exe"
        bin_path.write_bytes(b"\x4d\x5a\x90\x00\x03\x00\x00\x00\x04\x00\x00\x00")
        self.assertTrue(is_binary_file(bin_path))

    def test_decode_text_preserves_characters_and_normalizes_endings(self):
        # 1. UTF-8
        raw_utf8 = "func Main() {\n\tprintln(\"Hello\")\n}\n".encode("utf-8")
        self.assertEqual(decode_text(raw_utf8), "func Main() {\n\tprintln(\"Hello\")\n}\n")

        # 2. Windows CRLF normalization
        crlf_bytes = b"Line 1\r\nLine 2\r\nLine 3\r\n"
        decoded = decode_text(crlf_bytes)
        self.assertNotIn("\r", decoded)
        self.assertEqual(decoded, "Line 1\nLine 2\nLine 3\n")

        # 3. Windows-1252 / Latin-1 copyright symbol \xa9 and em-dash \x97
        cp1252_bytes = b"/* Copyright \xa9 2026 UniCompute \x97 All rights reserved. */\n"
        decoded_cp1252 = decode_text(cp1252_bytes)
        self.assertIn("©", decoded_cp1252)
        self.assertIn("—", decoded_cp1252)
        self.assertNotIn("\ufffd", decoded_cp1252)

        # 4. UTF-16 LE with BOM
        utf16_bytes = "Write-Host 'UniCompute Worker'\n".encode("utf-16")
        decoded_utf16 = decode_text(utf16_bytes)
        self.assertEqual(decoded_utf16, "Write-Host 'UniCompute Worker'\n")

    def test_clean_notebook_content(self):
        sample_nb = {
            "cells": [
                {
                    "cell_type": "code",
                    "source": ["import math\n", "print(math.pi)"],
                    "outputs": [{"output_type": "stream", "text": ["3.14159\n"]}],
                    "execution_count": 5
                },
                {
                    "cell_type": "markdown",
                    "source": ["# Analysis"]
                }
            ],
            "metadata": {},
            "nbformat": 4,
            "nbformat_minor": 2
        }
        # Standard UTF-8 JSON
        raw_bytes = json.dumps(sample_nb).encode("utf-8")
        cleaned, was_cleaned = clean_notebook_content(raw_bytes)
        self.assertTrue(was_cleaned)
        cleaned_json = json.loads(cleaned)
        code_cell = cleaned_json["cells"][0]
        self.assertEqual(code_cell["outputs"], [])
        self.assertIsNone(code_cell["execution_count"])
        self.assertEqual(code_cell["source"], ["import math\n", "print(math.pi)"])

        # UTF-8 with BOM JSON notebook
        raw_bom_bytes = b"\xef\xbb\xbf" + json.dumps(sample_nb).encode("utf-8")
        cleaned_bom, was_cleaned_bom = clean_notebook_content(raw_bom_bytes)
        self.assertTrue(was_cleaned_bom)
        cleaned_bom_json = json.loads(cleaned_bom)
        self.assertEqual(cleaned_bom_json["cells"][0]["outputs"], [])

    def test_discover_project_files_inclusions_and_exclusions(self):
        # Create allowed files
        (self.root / "pkg" / "compute").mkdir(parents=True)
        (self.root / "pkg" / "compute" / "node.go").write_text("package compute\n", encoding="utf-8")
        (self.root / "cmd" / "app").mkdir(parents=True)
        (self.root / "cmd" / "app" / "main.go").write_text("package main\n", encoding="utf-8")
        (self.root / "README.md").write_text("# Readme\n", encoding="utf-8")
        (self.root / ".gitignore").write_text("*.exe\nbin/\n", encoding="utf-8")

        # Create excluded directory files
        (self.root / ".git").mkdir()
        (self.root / ".git" / "config").write_text("git config", encoding="utf-8")
        (self.root / "bin").mkdir()
        (self.root / "bin" / "tool.sh").write_text("echo bin", encoding="utf-8")
        (self.root / ".tmp").mkdir()
        (self.root / ".tmp" / "temp.go").write_text("temp", encoding="utf-8")
        (self.root / "ui_extracted").mkdir()
        (self.root / "ui_extracted" / "mockup.html").write_text("<html></html>", encoding="utf-8")

        # Create excluded extension and file patterns
        (self.root / "test.exe").write_bytes(b"\x4d\x5a\x00\x00")
        (self.root / "report.docx").write_bytes(b"PK\x03\x04")
        (self.root / "archive.zip").write_bytes(b"PK\x03\x04")
        (self.root / "GEMINI.md").write_text("agent prompt", encoding="utf-8")
        (self.root / "digest.txt").write_text("old digest", encoding="utf-8")
        (self.root / ".DS_Store").write_bytes(b"junk")

        discovered = discover_project_files(self.root, output_path="digest.txt")
        self.assertIn("cmd/app/main.go", discovered)
        self.assertIn("pkg/compute/node.go", discovered)
        self.assertIn("README.md", discovered)
        self.assertIn(".gitignore", discovered)

        # Ensure exclusions are absent
        self.assertNotIn(".git/config", discovered)
        self.assertNotIn("bin/tool.sh", discovered)
        self.assertNotIn(".tmp/temp.go", discovered)
        self.assertNotIn("ui_extracted/mockup.html", discovered)
        self.assertNotIn("test.exe", discovered)
        self.assertNotIn("report.docx", discovered)
        self.assertNotIn("archive.zip", discovered)
        self.assertNotIn("GEMINI.md", discovered)
        self.assertNotIn("digest.txt", discovered)
        self.assertNotIn(".DS_Store", discovered)

    def test_include_ui_flag(self):
        (self.root / "ui_extracted").mkdir()
        (self.root / "ui_extracted" / "mockup.html").write_text("<html></html>", encoding="utf-8")
        (self.root / "main.go").write_text("package main", encoding="utf-8")

        # Without include_ui
        discovered_default = discover_project_files(self.root, include_ui=False)
        self.assertNotIn("ui_extracted/mockup.html", discovered_default)

        # With include_ui
        discovered_ui = discover_project_files(self.root, include_ui=True)
        self.assertIn("ui_extracted/mockup.html", discovered_ui)

    def test_pack_project_end_to_end_with_multi_encoding_and_utf16(self):
        (self.root / "pkg").mkdir()
        (self.root / "pkg" / "a.go").write_text("package pkg // Line 1\r\n// Line 2\r\n", encoding="utf-8")
        (self.root / "cmd").mkdir()
        (self.root / "cmd" / "main.go").write_text("package main // Main\n", encoding="utf-8")
        (self.root / "script.ps1").write_bytes("Write-Host 'Hello from PS'\r\n".encode("utf-16"))
        (self.root / "license.txt").write_bytes(b"/* Copyright \xa9 2026 UniCompute. */\n")
        (self.root / "binary.dat").write_bytes(b"ELF\x00\x01\x02\x00some_binary_bytes")

        out_file = self.root / "output_digest.txt"
        res = pack_project(self.root, output_target=out_file)

        self.assertEqual(res["total_discovered"], 5)
        self.assertEqual(res["included_count"], 4)
        self.assertEqual(len(res["skipped"]), 1)
        self.assertEqual(res["skipped"][0], ("binary.dat", "binary"))

        self.assertTrue(out_file.exists())
        content = out_file.read_text(encoding="utf-8")

        # Verify no raw carriage returns in output (normalized line endings)
        self.assertNotIn("\r\n", content)

        # Verify directory structure
        self.assertIn("Directory structure:", content)
        self.assertIn("  cmd/main.go", content)
        self.assertIn("  license.txt", content)
        self.assertIn("  pkg/a.go", content)
        self.assertIn("  script.ps1", content)

        # Verify contents and encodings
        self.assertIn("FILE: cmd/main.go", content)
        self.assertIn("package main // Main", content)
        self.assertIn("FILE: license.txt", content)
        self.assertIn("/* Copyright © 2026 UniCompute. */", content)
        self.assertIn("FILE: script.ps1", content)
        self.assertIn("Write-Host 'Hello from PS'", content)

    def test_pack_project_prevents_recursive_digest_packing_with_arbitrary_names(self):
        (self.root / "code.py").write_text("print('test')\n", encoding="utf-8")

        # 1. First packing into an arbitrary name (NOT digest.txt)
        custom_digest1 = self.root / "repo_snapshot_v1.txt"
        pack_project(self.root, output_target=custom_digest1)
        self.assertTrue(custom_digest1.exists())

        # 2. Second packing into another arbitrary name
        custom_digest2 = self.root / "repo_snapshot_v2.txt"
        res2 = pack_project(self.root, output_target=custom_digest2)

        # custom_digest1 MUST be recognized by content as an existing digest and skipped!
        self.assertNotIn("repo_snapshot_v1.txt", res2["included_files"])
        self.assertIn(("repo_snapshot_v1.txt", "prior_digest"), res2["skipped"])
        self.assertEqual(res2["included_files"], ["code.py"])

    def test_max_file_size_cap(self):
        (self.root / "small.txt").write_text("small file", encoding="utf-8")
        # Create a 300KB file
        (self.root / "large.txt").write_text("x" * (300 * 1024), encoding="utf-8")

        out_file = self.root / "digest.txt"
        # Set max size to 200KB
        res = pack_project(self.root, output_target=out_file, max_file_size_kb=200)

        self.assertIn("small.txt", res["included_files"])
        self.assertNotIn("large.txt", res["included_files"])
        self.assertTrue(any(s[0] == "large.txt" and "oversized" in s[1] for s in res["skipped"]))


if __name__ == "__main__":
    unittest.main()
