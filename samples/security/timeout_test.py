#!/usr/bin/env python3
"""
UniCompute Security Test: Execution Timeout Enforcement
Tests worker enforcement of container runtime bounds (default 10s - 15s).
This script deliberately executes an infinite loop with sleep to verify
that the worker forcibly kills the container and reports a timeout status.
"""

import time
import sys

print("[SECURITY TEST] Starting infinite compute/sleep loop...", flush=True)
print("[SECURITY TEST] Worker policy must terminate container after timeout.", flush=True)

elapsed = 0
while True:
    time.sleep(1)
    elapsed += 1
    print(f"[SECURITY TEST] Elapsed: {elapsed} seconds (still running)...", flush=True)
