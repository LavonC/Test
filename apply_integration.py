#!/usr/bin/env python3
"""Apply the UniCompute Docker code-execution CLI integration to an existing checkout."""
from __future__ import annotations

import argparse
import re
import shutil
import sys
from pathlib import Path


def replace_once(path: Path, old: str, new: str, description: str) -> None:
    text = path.read_text(encoding="utf-8")
    ORIGINALS.setdefault(path, text)
    count = text.count(old)
    if count != 1:
        raise RuntimeError(f"{path.relative_to(ROOT)}: expected exactly one match for {description}, found {count}")
    path.write_text(text.replace(old, new, 1), encoding="utf-8")


def regex_once(path: Path, pattern: str, replacement: str, description: str) -> None:
    text = path.read_text(encoding="utf-8")
    ORIGINALS.setdefault(path, text)
    changed, count = re.subn(pattern, replacement, text, count=1, flags=re.MULTILINE)
    if count != 1:
        raise RuntimeError(f"{path.relative_to(ROOT)}: expected one match for {description}, found {count}")
    path.write_text(changed, encoding="utf-8")


def copy_bundle_files(bundle_root: Path, repo_root: Path) -> None:
    """Copy the bundle's new source files when the script is run from elsewhere."""
    relative_files = [
        Path("pkg/codeexec/types.go"),
        Path("pkg/codeexec/docker.go"),
        Path("pkg/codeexec/types_test.go"),
        Path("pkg/node/codeexec.go"),
        Path("cmd/unicompute-node/console_args.go"),
        Path("README_CODE_EXECUTION_INTEGRATION.md"),
    ]
    for relative in relative_files:
        source = bundle_root / relative
        destination = repo_root / relative
        if source.resolve() == destination.resolve():
            continue
        if not source.is_file():
            raise RuntimeError(f"bundle file is missing: {source}")
        destination.parent.mkdir(parents=True, exist_ok=True)
        if destination.exists():
            if destination.read_bytes() != source.read_bytes():
                raise RuntimeError(f"refusing to overwrite existing file: {destination}; compare it with {source}")
            continue
        shutil.copy2(source, destination)


def main() -> int:
    global ROOT
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parent,
                        help="Path to the UniCompute repository root (default: this script's directory)")
    args = parser.parse_args()
    ROOT = args.repo.resolve()
    copy_bundle_files(Path(__file__).resolve().parent, ROOT)

    required = [
        ROOT / "go.mod",
        ROOT / "pkg" / "node" / "node.go",
        ROOT / "pkg" / "transport" / "manager.go",
        ROOT / "cmd" / "unicompute-node" / "main.go",
    ]
    for p in required:
        if not p.is_file():
            print(f"ERROR: {p} not found. Extract this bundle into the repository root or pass --repo.", file=sys.stderr)
            return 2
    if "module github.com/unicompute/obj1" not in (ROOT / "go.mod").read_text(encoding="utf-8"):
        print("ERROR: this does not look like github.com/unicompute/obj1.", file=sys.stderr)
        return 2

    node = ROOT / "pkg" / "node" / "node.go"
    main_go = ROOT / "cmd" / "unicompute-node" / "main.go"
    manager = ROOT / "pkg" / "transport" / "manager.go"

    # node.Node stores the optional executor and the pending result channels.
    replace_once(
        node,
        '\t"github.com/unicompute/obj1/pkg/blobstore"\n',
        '\t"github.com/unicompute/obj1/pkg/blobstore"\n\t"github.com/unicompute/obj1/pkg/codeexec"\n',
        "node.go codeexec import",
    )
    regex_once(
        node,
        r'^([ \t]*shellExecutor[ \t]+remoteshell\.Executor[ \t]*)$',
        r'\1\n\tcodeExecutor      codeexec.Executor\n\tcodeAllowedPeers map[string]struct{}\n\tpendingCodeMu    sync.Mutex\n\tpendingCode      map[string]pendingCodeResult',
        "Node code execution fields",
    )
    replace_once(
        node,
        '\t\tshellExecutor: remoteshell.NewSystemExecutor(),\n',
        '\t\tshellExecutor:   remoteshell.NewSystemExecutor(),\n\t\tcodeAllowedPeers: make(map[string]struct{}),\n\t\tpendingCode:      make(map[string]pendingCodeResult),\n',
        "Node constructor code execution maps",
    )
    replace_once(
        node,
        'func (n *Node) handleInternalStreamMessage(peerID string, data []byte) bool {\n',
        'func (n *Node) handleInternalStreamMessage(peerID string, data []byte) bool {\n\tif n.handleCodeStreamMessage(peerID, data) {\n\t\treturn true\n\t}\n',
        "node code message dispatch",
    )

    # Add CLI flags and register the Docker executor only when explicitly opted in.
    replace_once(
        main_go,
        '\t"github.com/unicompute/obj1/pkg/node"\n',
        '\t"github.com/unicompute/obj1/pkg/node"\n\t"github.com/unicompute/obj1/pkg/codeexec"\n',
        "CLI codeexec import",
    )
    replace_once(
        main_go,
        '\trunRelay := flag.Bool("run-relay", false, "Contribute relay bandwidth to the fleet (runs local contributor relay)")\n',
        '\trunRelay := flag.Bool("run-relay", false, "Contribute relay bandwidth to the fleet (runs local contributor relay)")\n'
        '\tenableCodeExecution := flag.Bool("enable-code-execution", false, "Enable Docker sandbox code execution on this worker")\n'
        '\tcodeAllowlist := flag.String("code-allowlist", "", "Comma-separated node IDs allowed to submit code jobs")\n'
        '\tcodeTimeout := flag.Int("code-timeout", 10, "Code job timeout in seconds (1-30)")\n',
        "CLI code execution flags",
    )
    replace_once(
        main_go,
        '\tflag.Parse()\n',
        '\tflag.Parse()\n\tif *codeTimeout < 1 || *codeTimeout > codeexec.MaxTimeoutSeconds {\n'
        '\t\tlog.Fatalf("-code-timeout must be between 1 and %d seconds", codeexec.MaxTimeoutSeconds)\n\t}\n',
        "CLI code timeout validation",
    )
    replace_once(
        main_go,
        '\tdefer n.Close()\n',
        '\tif *enableCodeExecution {\n'
        '\t\tvar allowedPeers []string\n'
        '\t\tfor _, peerID := range strings.Split(*codeAllowlist, ",") {\n'
        '\t\t\tif peerID = strings.TrimSpace(peerID); peerID != "" {\n'
        '\t\t\t\tallowedPeers = append(allowedPeers, peerID)\n\t\t\t}\n\t\t}\n'
        '\t\tif len(allowedPeers) == 0 {\n'
        '\t\t\tlog.Fatalf("-enable-code-execution requires -code-allowlist with one or more trusted peer IDs")\n\t\t}\n'
        '\t\tdockerCtx, cancelDocker := context.WithTimeout(context.Background(), 5*time.Second)\n'
        '\t\tdockerErr := codeexec.CheckDocker(dockerCtx)\n\t\tcancelDocker()\n'
        '\t\tif dockerErr != nil {\n\t\t\tlog.Fatalf("code execution requested but Docker is unavailable: %v", dockerErr)\n\t\t}\n'
        '\t\tn.SetCodeExecutor(codeexec.NewDockerExecutor(codeexec.Config{\n'
        '\t\t\tDefaultTimeout: time.Duration(*codeTimeout) * time.Second,\n'
        '\t\t\tMaxTimeout:     time.Duration(codeexec.MaxTimeoutSeconds) * time.Second,\n'
        '\t\t\tMaxConcurrent:  2,\n\t\t}), allowedPeers...)\n'
        '\t\tlog.Printf("[CODEEXEC] Docker sandbox enabled; allowed submitting peers: %s", strings.Join(allowedPeers, ", "))\n'
        '\t}\n\tdefer n.Close()\n',
        "CLI worker executor setup (first defer n.Close)",
    )

    # Quoted paths make the new run command practical in the existing terminal.
    replace_once(main_go, '\t\t\tparts := strings.Fields(line)\n', '\t\t\tparts := splitConsoleArgs(line)\n', "quoted terminal argument parsing")
    replace_once(
        main_go,
        '\t\t\t\tfmt.Println("  shell <peerID|all> <cmd...>    - Alias for remote command execution")\n',
        '\t\t\t\tfmt.Println("  shell <peerID|all> <cmd...>    - Alias for remote command execution")\n'
        '\t\t\t\tfmt.Println("  run <peerID> <c|cpp|python> <source-file> [stdin-file] - Run source code in a Docker sandbox")\n',
        "help text for run command",
    )
    run_case = r'''\t\t\tcase "run":
\t\t\t\tif len(parts) < 4 || len(parts) > 5 {
\t\t\t\t\tfmt.Println("Usage: run <peerID> <c|cpp|python> <source-file> [stdin-file]")
\t\t\t\t\tfmt.Println(`Example: run node-b python "./hello world.py"`)
\t\t\t\t\tbreak
\t\t\t\t}
\t\t\t\tpeerID := parts[1]
\t\t\t\tlanguage := strings.ToLower(parts[2])
\t\t\t\tif language != "c" && language != "cpp" && language != "python" {
\t\t\t\t\tfmt.Println("Supported languages: c, cpp, python")
\t\t\t\t\tbreak
\t\t\t\t}
\t\t\t\tfileInfo, statErr := os.Stat(parts[3])
\t\t\t\tif statErr != nil {
\t\t\t\t\tfmt.Printf("Cannot access source file: %v\\n", statErr)
\t\t\t\t\tbreak
\t\t\t\t}
\t\t\t\tif !fileInfo.Mode().IsRegular() || fileInfo.Size() > int64(codeexec.MaxSourceBytes) {
\t\t\t\t\tfmt.Printf("Source file must be a regular file no larger than %d bytes.\\n", codeexec.MaxSourceBytes)
\t\t\t\t\tbreak
\t\t\t\t}
\t\t\t\tsourceBytes, readErr := os.ReadFile(parts[3])
\t\t\t\tif readErr != nil {
\t\t\t\t\tfmt.Printf("Cannot read source file: %v\\n", readErr)
\t\t\t\t\tbreak
\t\t\t\t}
\t\t\t\tstdin := ""
\t\t\t\tif len(parts) == 5 {
\t\t\t\t\tinputInfo, inputStatErr := os.Stat(parts[4])
\t\t\t\t\tif inputStatErr != nil {
\t\t\t\t\t\tfmt.Printf("Cannot access stdin file: %v\\n", inputStatErr)
\t\t\t\t\t\tbreak
\t\t\t\t\t}
\t\t\t\t\tif !inputInfo.Mode().IsRegular() || inputInfo.Size() > int64(codeexec.MaxStdinBytes) {
\t\t\t\t\t\tfmt.Printf("Stdin file must be a regular file no larger than %d bytes.\\n", codeexec.MaxStdinBytes)
\t\t\t\t\t\tbreak
\t\t\t\t\t}
\t\t\t\t\tinputBytes, inputReadErr := os.ReadFile(parts[4])
\t\t\t\t\tif inputReadErr != nil {
\t\t\t\t\t\tfmt.Printf("Cannot read stdin file: %v\\n", inputReadErr)
\t\t\t\t\t\tbreak
\t\t\t\t\t}
\t\t\t\t\tstdin = string(inputBytes)
\t\t\t\t}
\t\t\t\treq := codeexec.Request{
\t\t\t\t\tJobID: "job-" + uuid.New().String(), Language: language,
\t\t\t\t\tSource: string(sourceBytes), Stdin: stdin, TimeoutSeconds: *codeTimeout,
\t\t\t\t}
\t\t\t\tfmt.Printf("[RUN] Submitting %s job %s to %s...\\n", language, req.JobID, peerID)
\t\t\t\tjobStart := time.Now()
\t\t\t\texecCtx, cancelExec := context.WithTimeout(context.Background(), time.Duration(*codeTimeout+8)*time.Second)
\t\t\t\tresp, execErr := n.ExecuteRemoteCode(execCtx, peerID, req)
\t\t\t\tcancelExec()
\t\t\t\tif execErr != nil {
\t\t\t\t\tfmt.Printf("[RUN] Job failed: %v\\n", execErr)
\t\t\t\t\tbreak
\t\t\t\t}
\t\t\t\tfmt.Println("=================================================================")
\t\t\t\tfmt.Printf(" Job ID: %s | Worker: %s | Language: %s\\n", resp.JobID, peerID, resp.Language)
\t\t\t\tif resp.TimedOut {
\t\t\t\t\tfmt.Println(" Status: Timed out")
\t\t\t\t} else if resp.ExitCode == 0 && resp.Error == "" {
\t\t\t\t\tfmt.Println(" Status: Completed")
\t\t\t\t} else {
\t\t\t\t\tfmt.Println(" Status: Failed")
\t\t\t\t}
\t\t\t\tif resp.Error != "" { fmt.Printf(" Error: %s\\n", resp.Error) }
\t\t\t\tif resp.Stdout != "" {
\t\t\t\t\tfmt.Println(" --- STDOUT ---")
\t\t\t\t\tfmt.Print(resp.Stdout)
\t\t\t\t\tif !strings.HasSuffix(resp.Stdout, "\\n") { fmt.Println() }
\t\t\t\t}
\t\t\t\tif resp.Stderr != "" {
\t\t\t\t\tfmt.Println(" --- STDERR ---")
\t\t\t\t\tfmt.Print(resp.Stderr)
\t\t\t\t\tif !strings.HasSuffix(resp.Stderr, "\\n") { fmt.Println() }
\t\t\t\t}
\t\t\t\tif resp.OutputTruncated { fmt.Println(" [!] Output was truncated by the worker's output limit.") }
\t\t\t\tfmt.Printf(" Exit code: %d | Worker execution: %d ms | Round trip: %d ms\\n", resp.ExitCode, resp.DurationMs, time.Since(jobStart).Milliseconds())
\t\t\t\tfmt.Println("=================================================================")
'''.replace('\\t', '\t')
    # The raw block above is intentionally used to retain Go tabs; normalize escaped newlines in the generated Go source.
    run_case = run_case.replace('\\\\n', '\\n')
    replace_once(
        main_go,
        '\t\t\tcase "quit", "exit":\n',
        run_case + '\t\t\tcase "quit", "exit":\n',
        "CLI run command case",
    )

    # Control and code-job messages must be reliably delivered. The original
    # stream channel allowed packet loss after a small retransmission count.
    replace_once(
        manager,
        '\t// Create Dual DataChannels:\n\t// Channel A: "stream" (ordered, maxRetransmits=3)\n'
        '\tmaxRetransmits := uint16(3)\n'
        '\tstreamDC, err := pc.CreateDataChannel("stream", &webrtc.DataChannelInit{\n'
        '\t\tOrdered:        func(b bool) *bool { return &b }(true),\n'
        '\t\tMaxRetransmits: &maxRetransmits,\n'
        '\t})',
        '\t// Create Dual DataChannels:\n'
        '\t// Channel A: "stream" is reliable because it carries control and job messages.\n'
        '\tstreamDC, err := pc.CreateDataChannel("stream", &webrtc.DataChannelInit{\n'
        '\t\tOrdered: func(b bool) *bool { return &b }(true),\n'
        '\t})',
        "reliable stream DataChannel",
    )

    print("Integration applied. New files were already extracted into place.")
    print("Next: run gofmt and go test ./..., then follow README_CODE_EXECUTION_INTEGRATION.md.")
    return 0


ROOT = Path.cwd()
ORIGINALS: dict[Path, str] = {}

if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        for path, original in ORIGINALS.items():
            path.write_text(original, encoding="utf-8")
        print(f"ERROR: {exc}", file=sys.stderr)
        print("Existing source-file edits were rolled back. The newly added files were left in place.", file=sys.stderr)
        raise SystemExit(1)
