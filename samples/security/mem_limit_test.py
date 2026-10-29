#!/usr/bin/env python3
"""
UniCompute Security Test: Memory Limit Enforcement (128 MB Cap)
Verifies that the Docker container sandbox enforces '--memory 128m --memory-swap 128m'.
Continuously allocates 10 MB blocks in RAM until the container is capped
by the Linux OOM (Out Of Memory) killer or raises MemoryError.
"""

import sys
import time

print("==================================================")
print("  UniCompute Security: 128 MB RAM Limit Audit     ")
print("==================================================")
print("Allocating memory in 10 MB chunks...")

chunks = []
total_mb = 0

try:
    for step in range(1, 30):
        # Allocate 10 MB of real dirty pages (not just virtual space)
        chunk = bytearray(10 * 1024 * 1024)
        # Touch every page so the kernel actually commits the RAM
        for i in range(0, len(chunk), 4096):
            chunk[i] = 0xFF
        chunks.append(chunk)
        total_mb += 10
        print(f"[ALLOCATION] Successfully allocated {total_mb} MB RAM", flush=True)
        time.sleep(0.05)

    print(f"[FAIL] Allocated {total_mb} MB without hitting the 128 MB container cap!")
    sys.exit(1)

except MemoryError:
    print(f"\n[PASS] Python raised MemoryError after allocating {total_mb} MB.")
    print("RESULT: MEMORY CAPPING ENFORCED SUCCESSFULLY.")
    sys.exit(0)
