#!/usr/bin/env python3
"""
UniCompute Objective 2 Sample: Matrix Multiplication Benchmark
Demonstrates floating-point matrix multiplication with validation and optional stdin dimension override.
"""
import sys
import time

def matrix_multiply(dim=64):
    print("==================================================")
    print(" UniCompute Worker: Matrix Multiplication (O2)   ")
    print("==================================================")
    
    # Read matrix dimension from stdin if provided
    raw = sys.stdin.read().strip()
    if raw.isdigit():
        dim = max(8, min(256, int(raw)))
        print(f"[INPUT] Using stdin matrix dimension: {dim}x{dim}")
    else:
        print(f"[INFO] Using default dimension: {dim}x{dim}")

    print(f"Allocating two {dim}x{dim} floating-point matrices...")
    A = [[(i * dim + j) * 0.001 for j in range(dim)] for i in range(dim)]
    B = [[(j * dim + i) * 0.001 for j in range(dim)] for i in range(dim)]
    C = [[0.0 for _ in range(dim)] for _ in range(dim)]

    t0 = time.time()
    for i in range(dim):
        for k in range(dim):
            aik = A[i][k]
            for j in range(dim):
                C[i][j] += aik * B[k][j]
    elapsed = time.time() - t0

    # Verification checksum
    trace = sum(C[i][i] for i in range(dim))
    corner_sum = C[0][0] + C[0][-1] + C[-1][0] + C[-1][-1]
    gflops = (2.0 * dim * dim * dim) / (elapsed * 1e9) if elapsed > 0 else 0.0

    print("--------------------------------------------------")
    print(f"Matrix Dimension : {dim} x {dim}")
    print(f"Total Operations : {2 * dim**3:,} FLOPs")
    print(f"Compute Duration : {elapsed * 1000.0:.2f} ms")
    print(f"Performance      : {gflops * 1000.0:.2f} MFLOP/s")
    print(f"Matrix Trace     : {trace:.6f}")
    print(f"Corners Checksum : {corner_sum:.6f}")
    print("==================================================")
    print("Status: Matrix multiplication completed successfully.")

if __name__ == "__main__":
    matrix_multiply()
