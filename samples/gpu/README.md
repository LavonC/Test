# Objective 3 GPU Test Sample Data & Verification Suite

This directory contains test kernels and workload inputs for validating **UniCompute Objective 3 (Cross-Platform GPU Acceleration and Resource Governance)**.

---

## Sample Data Files

| File | Type | Target Hardware | Purpose & Expected Behavior |
|---|---|---|---|
| `portable_vector_add.cu` | CUDA C++ Kernel | NVIDIA & AMD | Standard portable vector addition. Tests automated HIPIFY translation (`cudaMalloc` $\rightarrow$ `hipMalloc`, `cudaFree` $\rightarrow$ `hipFree`). Marked **PASS/Portable** for AMD execution. |
| `warp_reduction.cu` | CUDA C++ Kernel | NVIDIA Only | Implements tree reduction using `__shfl_down_sync` and `threadIdx.x % 32`. Triggers static analysis blocker on AMD 64-thread wavefronts. Marked **REJECTED** with diagnostic line pointers. |
| `inline_ptx.cu` | CUDA C++ Kernel | NVIDIA Only | Contains inline PTX assembly (`asm volatile("mov.u32 %0, %smid;")`). Triggers ROCm compiler incompatibility blocker. Marked **REJECTED** with recommendation to export to ONNX. |
| `test_model.json` | ONNX Metadata | DirectML & CUDA | Tensor layer definition and weights for validating forward FP32 preprocessing and execution. |

---

## Quick Verification Commands

### 1. Test Automated CUDA Translation
```text
> hipify ./samples/gpu/portable_vector_add.cu
```
*Expected*: Success report, 7 replacements made, preview of translated `#include <hip/hip_runtime.h>` code.

### 2. Test Warp Incompatibility Detection
```text
> hipify ./samples/gpu/warp_reduction.cu
```
*Expected*: Diagnostic warning on Line 4 (`threadIdx.x % 32`) and Line 9 (`__shfl_down_sync`), blocking translation to prevent runtime errors on AMD GPUs.

### 3. Test Inline PTX Detection
```text
> hipify ./samples/gpu/inline_ptx.cu
```
*Expected*: Diagnostic error on Line 6 flagging inline PTX assembly.

### 4. Test JobObject Sandboxed Execution
```text
> gpu-run cpp ./samples/gpu/portable_vector_add.cu 50
```
*Expected*: Execution completes inside the Win32 JobObject sandbox under `IDLE_PRIORITY_CLASS` with bounded memory.
