# UniCompute remote code-execution integration

This bundle adds a `run` command to the existing UniCompute node CLI. A user reads a source file on their own computer, then sends a small job over the existing WebRTC `stream` DataChannel to a connected worker. The worker executes C, C++, or Python in a short-lived Docker container and sends stdout/stderr, exit code, timeout status, and duration back to the caller.

## What is added or changed

New files (extract these into the repository root so the paths below line up):

- `pkg/codeexec/types.go` — bounded code-job request/response format and DataChannel message prefixes.
- `pkg/codeexec/docker.go` — Docker-backed executor with network disabled, read-only root filesystem, unprivileged UID, CPU/memory/PID limits, temporary filesystem, output caps, timeout, and cleanup.
- `pkg/codeexec/types_test.go` — protocol and size-limit tests.
- `pkg/node/codeexec.go` — remote request/response routing, result correlation, worker opt-in and peer-ID allowlist.
- `cmd/unicompute-node/console_args.go` — quoted path parsing for the interactive terminal.
- `apply_integration.py` — adds the fields, imports, CLI flags, `run` command and reliable stream channel setting to existing files.

Existing files changed by the installer:

- `pkg/node/node.go` — imports codeexec, stores executor/allowlist/pending job state, and dispatches code-job messages.
- `cmd/unicompute-node/main.go` — adds `-enable-code-execution`, `-code-allowlist`, `-code-timeout`, help text and `run` command.
- `pkg/transport/manager.go` — makes the stream DataChannel reliable. Code-job request/response messages must not be sent on the current lossy (three-retransmission) channel.

## Apply the integration

1. Commit or back up your current changes first.
2. Extract this bundle directly into your `Test` repository root. The new `pkg/` and `cmd/` files should merge into the existing folders.
3. From the repository root, run:

   ```bash
   python apply_integration.py
   gofmt -w pkg/codeexec/*.go pkg/node/codeexec.go cmd/unicompute-node/*.go
   go test ./...
   ```

The script checks the expected source anchors and rolls back edits to existing files if an anchor does not match. Run it once. Inspect `git diff` before committing.

## Prepare the worker

The worker machine must have Go and Docker installed, and Docker must be running in Linux-container mode. Pull the runtime images once (the worker will use these images):

```bash
docker pull gcc:14
docker pull python:3.13-slim
```

Give each node a stable ID. For example, start the worker node with code execution enabled and allow only `node-A` to submit jobs:

```bash
go run ./cmd/unicompute-node -id node-B -signaling ws://COORDINATOR_HOST:8080/ws -enable-code-execution -code-allowlist node-A -code-timeout 10
```

Replace `COORDINATOR_HOST` with the reachable coordinator hostname or IP. The `-code-allowlist` value must match the submitting node's configured ID exactly. Multiple IDs can be comma-separated, such as `-code-allowlist node-A,node-C`.

The requester starts normally, using the same coordinator URL:

```bash
go run ./cmd/unicompute-node -id node-A -signaling ws://COORDINATOR_HOST:8080/ws
```

From the requester terminal, connect to the worker and submit a local source file:

```text
> connect node-B
> run node-B python ./hello.py
> run node-B c ./hello.c
> run node-B cpp ./hello.cpp
```

An optional stdin file can be supplied as the fifth argument:

```text
> run node-B python ./read_input.py ./input.txt
```

Paths containing spaces can be quoted:

```text
> run node-B python "./my examples/hello world.py"
```

The `run` command sends **the source-file contents**, not a path on the requester, so the worker does not need that source file to exist locally. It creates its own temporary source file and removes it after the container exits.

## Current starter limits

- Languages: `c`, `cpp`, `python`.
- Source: at most 12 KiB per job.
- Standard input: at most 4 KiB.
- Captured stdout and stderr: at most 3 KiB each.
- Requested execution timeout: 1–30 seconds.
- Default worker concurrency: two jobs.
- Docker runtime: C/C++ use `gcc:14`; Python uses `python:3.13-slim`.
- Source is sent as one DataChannel message, capped at 48 KiB. Large source files should later use the reliable chunk channel and a job manifest.

## Security cautions

This is a starting implementation for a trusted test fleet, not a guarantee that arbitrary hostile code is safe. The allowlist checks the peer ID presented by the existing UniCompute protocol; if node IDs are not cryptographically authenticated by the broader system, an ID allowlist alone is not strong identity verification. Do not enable the feature on a public/untrusted fleet without adding authenticated peer identity, rate limits, stronger sandboxing (such as gVisor/Firecracker), quotas and monitoring. Never mount `/var/run/docker.sock` or host directories into the job container. The executor mounts only its temporary source directory read-only.

The current repository also has a separate `exec`/`shell` feature that runs commands on the host. The new `run` command intentionally uses a separate protocol and Docker executor; do not use `exec` as a substitute for code execution.
