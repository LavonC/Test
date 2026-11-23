# language: Python, file: pack.py, runtime: Python 3.10+, target: UniCompute
"""
Pack project source code, configurations, scripts, and documentation into a consolidated digest file.
Designed for the UniCompute repository and multi-language software stacks.
"""

import argparse
import fnmatch
import json
import os
import sys
from pathlib import Path

# Directories excluded globally anywhere in the project tree
GLOBAL_EXCLUDE_DIRS = {
    # Version control & IDEs
    ".git", ".github", ".idea", ".vscode",
    # Python & test environments
    ".venv", "venv", "env",
    "__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache", ".tox",
    # Build outputs & temporary caches
    "build", "dist", "bin",
    ".gotmp", ".tmp", "tmp", "unicompute_data",
    # Frontend/Node caches
    "node_modules", ".next", ".nuxt",
    # Notebook checkpoints
    ".ipynb_checkpoints"
}

# Directories excluded only when located at the project root
ROOT_EXCLUDE_DIRS = {
    "data",
    "figures",
    "storage",
    "ui_extracted"  # Large extracted UI mockup suite (can be enabled via --include-ui)
}

# Specific filenames to always exclude
EXCLUDE_FILES = {
    "digest.txt",
    "Thumbs.db",
    "desktop.ini",
    ".DS_Store",
    "GEMINI.md"
}

# Standard configuration dotfiles that SHOULD be tracked/included
ALLOWED_DOTFILES = {
    ".gitignore",
    ".dockerignore",
    ".gitattributes",
    ".editorconfig",
    ".env.example"
}

# File extensions to exclude (binary, media, large data, archives, fonts)
EXCLUDE_EXTENSIONS = {
    # Data & database files
    ".parquet", ".csv", ".tsv", ".feather", ".h5", ".hdf5",
    ".pkl", ".pickle", ".joblib", ".db", ".sqlite", ".sqlite3",
    # Images, video, audio & media
    ".png", ".jpg", ".jpeg", ".gif", ".ico", ".svg", ".pdf",
    ".tiff", ".bmp", ".webp", ".mp4", ".mov", ".avi", ".mp3", ".wav",
    # Archives & compressed packages
    ".zip", ".tar", ".gz", ".7z", ".rar", ".bz2", ".xz",
    # Office documents
    ".docx", ".doc", ".xlsx", ".xls", ".pptx", ".ppt",
    # Compiled & binary files
    ".pyc", ".pyo", ".pyd",
    ".exe", ".exe~", ".dll", ".so", ".dylib", ".bin",
    ".whl", ".egg", ".class", ".o", ".obj", ".a", ".lib", ".iso",
    # Fonts
    ".woff", ".woff2", ".ttf", ".eot", ".otf"
}


def is_existing_digest(data: bytes) -> bool:
    """
    Checks if a file's content indicates it is an existing packed digest.
    Prevents recursive self-packing of previous digests regardless of filename.
    Handles UTF-8 BOM, CRLF/LF line endings, and arbitrarily large directory structures.
    """
    if not data:
        return False
    # Strip UTF-8 BOM if present
    if data.startswith(b"\xef\xbb\xbf"):
        data = data[3:]
    try:
        sample = data[:131072].decode("utf-8", errors="ignore")
        lines = [line for line in sample.splitlines() if line.strip()]
        if not lines:
            return False
        first_line = lines[0].strip()
        if first_line == "Directory structure:":
            # Verify digest characteristics: indented file tree or FILE delimiter
            if len(lines) > 1 and lines[1].startswith("  "):
                return True
            if "FILE: " in sample and "====" in sample:
                return True
        # Check standard separator headers in case of alternative or trimmed headers
        if any(line.startswith("================================================") for line in lines[:5]):
            if any(line.startswith("FILE: ") for line in lines[:10]):
                return True
    except Exception:
        pass
    return False


def is_binary_file(path: Path) -> bool:
    """
    Inspects the file header (first 8KB) to detect binary content.
    Prevents loading multi-megabyte binary files into RAM.
    UTF-16 LE/BE text files (which contain null bytes in ASCII range) are recognized
    via their BOM and correctly treated as text.
    """
    try:
        with open(path, "rb") as f:
            chunk = f.read(8192)
            if not chunk:
                return False
            # UTF-16 with BOM is text, not binary
            if chunk.startswith((b"\xff\xfe", b"\xfe\xff")):
                return False
            return b"\x00" in chunk
    except Exception:
        return True


def decode_text(data: bytes) -> str:
    """
    Decodes text bytes supporting UTF-8 (with/without BOM), UTF-16, CP1252, and Latin-1.
    Normalizes line endings to consistent LF (\n) to prevent mixed-line-ending files.
    """
    text = None
    # UTF-16 with BOM
    if data.startswith((b"\xff\xfe", b"\xfe\xff")):
        try:
            text = data.decode("utf-16")
        except UnicodeDecodeError:
            pass

    # UTF-8 with BOM or standard UTF-8
    if text is None:
        try:
            text = data.decode("utf-8-sig")
        except UnicodeDecodeError:
            pass

    # Windows-1252 (preserves smart quotes, em-dashes, copyright symbols)
    if text is None:
        try:
            text = data.decode("cp1252")
        except UnicodeDecodeError:
            pass

    # Latin-1 fallback
    if text is None:
        try:
            text = data.decode("latin-1")
        except UnicodeDecodeError:
            pass

    # Final fallback replacement
    if text is None:
        text = data.decode("utf-8", errors="replace")

    # Normalize line endings to LF
    return text.replace("\r\n", "\n").replace("\r", "\n")


def clean_notebook_content(data: bytes) -> tuple[str, bool]:
    """
    Cleans Jupyter Notebooks by stripping heavy cell outputs (e.g. base64 plots)
    and execution counts, preserving the code, markdown, and valid JSON structure.
    Returns (cleaned_text, was_cleaned).
    """
    text = decode_text(data)
    try:
        nb = json.loads(text)
        if isinstance(nb, dict) and "cells" in nb:
            has_outputs = False
            for cell in nb.get("cells", []):
                if cell.get("cell_type") == "code":
                    if cell.get("outputs") or cell.get("execution_count") is not None:
                        has_outputs = True
                        cell["outputs"] = []
                        cell["execution_count"] = None
            if has_outputs:
                cleaned = json.dumps(nb, indent=2)
                return cleaned.replace("\r\n", "\n"), True
    except Exception:
        pass
    return text, False


def discover_project_files(
    root_dir: Path,
    output_path: Path | str = "digest.txt",
    include_ui: bool = False
) -> list[str]:
    """
    Recursively discovers all necessary code, documentation, scripts, and configs in the project.
    Excludes virtual environments, caches, compiled binaries, binary documents, and ignored folders.
    """
    discovered = []
    root_resolved = root_dir.resolve()
    target_output_resolved = Path(output_path).resolve()

    root_excludes = set(ROOT_EXCLUDE_DIRS)
    if include_ui:
        root_excludes.discard("ui_extracted")

    for root, dirs, files in os.walk(root_resolved, topdown=True, followlinks=False):
        rel_root = Path(root).relative_to(root_resolved).as_posix()

        # In-place directory pruning
        pruned = []
        for d in dirs:
            d_lower = d.lower()
            if d.startswith("."):
                pruned.append(d)
                continue
            if d_lower in GLOBAL_EXCLUDE_DIRS:
                pruned.append(d)
                continue
            # Root-level only exclusions
            if rel_root == "." and d_lower in root_excludes:
                pruned.append(d)
                continue

        for p_dir in pruned:
            dirs.remove(p_dir)

        for fname in files:
            p = Path(root) / fname
            p_resolved = p.resolve()

            # Exclude target output digest file itself and permanently excluded filenames
            if p_resolved == target_output_resolved or fname in EXCLUDE_FILES:
                continue
            # Exclude hidden files except approved project configuration dotfiles
            if fname.startswith(".") and fname not in ALLOWED_DOTFILES:
                continue
            # Exclude by extension
            if p.suffix.lower() in EXCLUDE_EXTENSIONS:
                continue

            rel_posix = p.relative_to(root_resolved).as_posix()
            discovered.append(rel_posix)

    return sorted(discovered)


def pack_project(
    root_dir: Path,
    output_target: Path | str = "digest.txt",
    strip_notebook_outputs: bool = True,
    include_ui: bool = False,
    max_file_size_kb: int | None = 2048,
    verbose: bool = False
) -> dict:
    """
    Discovers, validates, and packs project code files into a single digest file.
    Returns a dictionary summarizing execution metrics.
    """
    root_resolved = root_dir.resolve()
    out_path = Path(output_target).resolve()

    files = discover_project_files(
        root_resolved,
        output_path=out_path,
        include_ui=include_ui
    )

    included_entries = []
    skipped = []

    for f in files:
        p = root_resolved / f
        if not p.is_file():
            skipped.append((f, "missing"))
            if verbose:
                print(f"[-] Skipped (missing): {f}")
            continue

        # File size cap check
        if max_file_size_kb is not None:
            try:
                size_kb = p.stat().st_size / 1024
                if size_kb > max_file_size_kb:
                    skipped.append((f, f"oversized_{size_kb:.0f}kb"))
                    if verbose:
                        print(f"[-] Skipped (oversized {size_kb:.0f}KB > {max_file_size_kb}KB): {f}")
                    continue
            except Exception as e:
                skipped.append((f, f"stat_error: {e}"))
                continue

        # Fast header check for binary files (prevents loading huge binaries into memory)
        if is_binary_file(p):
            skipped.append((f, "binary"))
            if verbose:
                print(f"[-] Skipped (binary content): {f}")
            continue

        try:
            data = p.read_bytes()
        except Exception as e:
            skipped.append((f, f"read_error: {e}"))
            if verbose:
                print(f"[-] Skipped (read error): {f}")
            continue

        # Check if the file is an existing digest output to avoid self-packing
        if is_existing_digest(data):
            skipped.append((f, "prior_digest"))
            if verbose:
                print(f"[-] Skipped (prior digest): {f}")
            continue

        # Clean notebook or decode text with robust encodings
        if f.lower().endswith(".ipynb") and strip_notebook_outputs:
            text, _ = clean_notebook_content(data)
        else:
            text = decode_text(data)

        included_entries.append((f, text))
        if verbose:
            print(f"[+] Included: {f}")

    # Ensure parent directory for output exists
    out_path.parent.mkdir(parents=True, exist_ok=True)

    # Write digest with normalized LF line endings
    with open(out_path, "w", encoding="utf-8", newline="\n") as out:
        out.write("Directory structure:\n")
        for f, _ in included_entries:
            out.write(f"  {f}\n")
        out.write("\n")

        for f, text in included_entries:
            out.write("=" * 48 + f"\nFILE: {f}\n" + "=" * 48 + "\n")
            out.write(text.rstrip("\n") + "\n\n")

    included_files = [f for f, _ in included_entries]
    total_lines = sum(text.count("\n") + 1 for _, text in included_entries)
    total_chars = sum(len(text) for _, text in included_entries)
    est_tokens = total_chars // 4

    # Language / category breakdown
    go_files = [f for f in included_files if f.endswith(".go")]
    c_cpp_files = [f for f in included_files if f.endswith((".c", ".cpp", ".h", ".hpp", ".cu", ".cuh", ".hip"))]
    py_files = [f for f in included_files if f.endswith((".py", ".ipynb"))]
    web_files = [f for f in included_files if f.endswith((".html", ".css", ".js", ".ts"))]
    script_files = [f for f in included_files if f.endswith((".ps1", ".sh", ".bat", ".cmd"))]
    config_files = [f for f in included_files if f.endswith((".yaml", ".yml", ".json", ".mod", ".sum", ".service")) or f in {".gitignore", "Dockerfile", "Caddyfile"}]
    doc_files = [f for f in included_files if f.endswith((".md", ".txt", ".rst"))]

    pkg_files = [f for f in included_files if f.startswith("pkg/")]
    cmd_files = [f for f in included_files if f.startswith("cmd/")]
    samples_files = [f for f in included_files if f.startswith("samples/")]
    deploy_files = [f for f in included_files if f.startswith("deploy/")]
    root_files = [f for f in included_files if "/" not in f]

    return {
        "output_path": out_path,
        "total_discovered": len(files),
        "included_count": len(included_files),
        "included_files": included_files,
        "total_lines": total_lines,
        "total_chars": total_chars,
        "est_tokens": est_tokens,
        "go_files": go_files,
        "c_cpp_files": c_cpp_files,
        "py_files": py_files,
        "web_files": web_files,
        "script_files": script_files,
        "config_files": config_files,
        "doc_files": doc_files,
        "pkg_files": pkg_files,
        "cmd_files": cmd_files,
        "samples_files": samples_files,
        "deploy_files": deploy_files,
        "root_files": root_files,
        "skipped": skipped
    }


def parse_args():
    parser = argparse.ArgumentParser(
        description="Pack project source code, tests, configs, and documentation into a single digest file."
    )
    parser.add_argument(
        "output",
        nargs="?",
        default="digest.txt",
        help="Path or filename for the output digest file (default: digest.txt)"
    )
    parser.add_argument(
        "-o", "--output-file", "--output",
        dest="flag_output",
        metavar="PATH",
        default=None,
        help="Alternative flag to specify output path"
    )
    parser.add_argument(
        "--root",
        metavar="DIR",
        default=None,
        help="Project root directory (default: directory containing pack.py)"
    )
    parser.add_argument(
        "--include-ui",
        action="store_true",
        help="Include ui_extracted directory in the digest (default: excluded)"
    )
    parser.add_argument(
        "--no-strip-notebooks",
        action="store_true",
        help="Do not strip cell execution outputs from Jupyter notebooks"
    )
    parser.add_argument(
        "--max-size-kb",
        type=int,
        metavar="KB",
        default=2048,
        help="Maximum individual file size in KB to pack (default: 2048, 0 for unlimited)"
    )
    parser.add_argument(
        "-v", "--verbose",
        action="store_true",
        help="Print verbose output for each file processed"
    )
    return parser.parse_args()


def main():
    args = parse_args()
    root_dir = Path(args.root).resolve() if args.root else Path(__file__).resolve().parent

    # If flag_output was explicitly given, use it; otherwise use positional output argument
    chosen_out = args.flag_output if args.flag_output else args.output
    out_path = Path(chosen_out).resolve()

    max_size = args.max_size_kb if args.max_size_kb > 0 else None

    res = pack_project(
        root_dir=root_dir,
        output_target=out_path,
        strip_notebook_outputs=not args.no_strip_notebooks,
        include_ui=args.include_ui,
        max_file_size_kb=max_size,
        verbose=args.verbose
    )

    out_file = res["output_path"]
    included = res["included_files"]
    skipped = res["skipped"]

    breakdown = []
    if res["pkg_files"]:
        breakdown.append(f"{len(res['pkg_files'])} in pkg/")
    if res["cmd_files"]:
        breakdown.append(f"{len(res['cmd_files'])} in cmd/")
    if res["samples_files"]:
        breakdown.append(f"{len(res['samples_files'])} in samples/")
    if res["deploy_files"]:
        breakdown.append(f"{len(res['deploy_files'])} in deploy/")
    if res["root_files"]:
        breakdown.append(f"{len(res['root_files'])} in root")

    breakdown_str = f" ({', '.join(breakdown)})" if breakdown else ""

    print(f"Total tracked: {res['total_discovered']}")
    print(f"Included:      {len(included)}{breakdown_str}")
    print(f"Lines of Code: {res['total_lines']:,} lines (~{res['est_tokens']:,} tokens)")
    if skipped:
        print(f"Skipped:       {len(skipped)} {skipped}")
    else:
        print("Skipped:       0")
    print(f"Digest output: {out_file} ({out_file.stat().st_size:,} bytes)")


if __name__ == "__main__":
    main()
