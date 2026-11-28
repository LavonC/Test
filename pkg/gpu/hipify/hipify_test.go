package hipify

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAnalyzeCUDASource_PortableKernel(t *testing.T) {
	portableSource := `
#include <cuda_runtime.h>
#include <stdio.h>

__global__ void vectorAdd(const float *a, const float *b, float *c, int n) {
    int i = blockDim.x * blockIdx.x + threadIdx.x;
    if (i < n) {
        c[i] = a[i] + b[i];
    }
}

int main() {
    float *d_a, *d_b, *d_c;
    cudaMalloc(&d_a, 1024 * sizeof(float));
    cudaFree(d_a);
    return 0;
}
`
	report := AnalyzeCUDASource(portableSource)
	if !report.IsPortable {
		t.Errorf("Expected kernel to be portable, got false with %d issues", len(report.Issues))
	}
	if !report.TargetAMDSupported {
		t.Errorf("Expected AMD support to be true")
	}

	result := TranslateCUDAToHIP(portableSource)
	if !result.Success {
		t.Fatalf("Expected translation to succeed: %s", result.Error)
	}
	if !strings.Contains(result.TranslatedSource, "hipMalloc") {
		t.Errorf("Expected hipMalloc in translated source")
	}
	if !strings.Contains(result.TranslatedSource, "hipFree") {
		t.Errorf("Expected hipFree in translated source")
	}
	if exe := FindHipifyClang(); exe != "" && result.Engine != "hipify-clang.exe" && result.Engine != "hipify-clang" {
		t.Errorf("Expected hipify-clang engine when binary is present (%s), got %q", exe, result.Engine)
	}
}

func TestAnalyzeCUDASource_WarpSyncIncompatibility(t *testing.T) {
	warpSource := `
__global__ void warpReduce(float *d_out, float *d_in) {
    int lane = threadIdx.x % 32;
    float val = d_in[threadIdx.x];
    for (int offset = 16; offset > 0; offset /= 2) {
        val += __shfl_down_sync(0xffffffff, val, offset);
    }
    if (lane == 0) d_out[blockIdx.x] = val;
}
`
	report := AnalyzeCUDASource(warpSource)
	if report.TargetAMDSupported {
		t.Errorf("Expected TargetAMDSupported to be false due to __shfl_down_sync and modulo 32")
	}
	if !report.HasWarpAssumptions {
		t.Errorf("Expected HasWarpAssumptions to be true")
	}

	result := TranslateCUDAToHIP(warpSource)
	if result.Success {
		t.Errorf("Expected translation to be rejected due to warp blocker")
	}
}

func TestAnalyzeCUDASource_InlinePTXIncompatibility(t *testing.T) {
	ptxSource := `
__device__ unsigned int getSmId() {
    unsigned int smid;
    asm volatile("mov.u32 %0, %smid;" : "=r"(smid));
    return smid;
}
`
	report := AnalyzeCUDASource(ptxSource)
	if report.TargetAMDSupported {
		t.Errorf("Expected TargetAMDSupported to be false due to inline PTX")
	}
	if !report.HasInlinePTX {
		t.Errorf("Expected HasInlinePTX to be true")
	}
}

func TestAnalyzeCUDASource_HostKernelLaunchNotDynamicParallelism(t *testing.T) {
	source := `
__global__ void myKernel(float *d_out) {
    int idx = threadIdx.x;
    d_out[idx] = 1.0f;
}

int main() {
    float *d_out;
    cudaMalloc(&d_out, 128 * sizeof(float));
    myKernel<<<1, 128>>>(d_out);
    cudaDeviceSynchronize();
    cudaFree(d_out);
    return 0;
}
`
	report := AnalyzeCUDASource(source)
	if report.HasDynamicLaunch {
		t.Errorf("Expected HasDynamicLaunch to be false for host kernel launch in main()")
	}
	if !report.TargetAMDSupported {
		t.Errorf("Expected TargetAMDSupported to be true for clean host kernel launch")
	}
}

func TestAnalyzeCUDASource_DeviceKernelLaunchDetectedAsDynamicParallelism(t *testing.T) {
	source := `
__global__ void childKernel() {
    // child work
}

__global__ void parentKernel() {
    childKernel<<<1, 16>>>();
}
`
	report := AnalyzeCUDASource(source)
	if !report.HasDynamicLaunch {
		t.Errorf("Expected HasDynamicLaunch to be true when kernel launched inside parentKernel")
	}
}

func TestAnalyzeCUDASource_InlinePTXWithoutVolatile(t *testing.T) {
	source := `
__device__ unsigned int getLane() {
    unsigned int lane;
    asm("mov.u32 %0, %%laneid;" : "=r"(lane));
    return lane;
}
`
	report := AnalyzeCUDASource(source)
	if !report.HasInlinePTX {
		t.Errorf("Expected HasInlinePTX to be true for asm(...) without volatile")
	}
	if report.TargetAMDSupported {
		t.Errorf("Expected TargetAMDSupported to be false for inline asm(...)")
	}
}

func TestAnalyzeCUDASource_MultilineBlockCommentsIgnored(t *testing.T) {
	source := `
/*
   Multi-line comment mentioning non-portable constructs:
   __shfl_down_sync(0xffffffff, val, 1);
   asm("mov.u32 %0, %smid;" : "=r"(smid));
   #include <cudnn.h>
   threadIdx.x % 32
*/
__global__ void cleanKernel(float *x) {
    /* inline block comment with asm("nop;") */
    int idx = blockIdx.x * blockDim.x + threadIdx.x;
    x[idx] *= 2.0f;
}
`
	report := AnalyzeCUDASource(source)
	if !report.IsPortable {
		t.Errorf("Expected commented constructs not to trigger issues, got %+v", report.Issues)
	}
	if !report.TargetAMDSupported {
		t.Errorf("Expected TargetAMDSupported to be true")
	}
	if report.HasInlinePTX || report.HasWarpAssumptions || report.HasProprietaryLibs {
		t.Errorf("Expected all blocker flags false, got ptx=%v warp=%v libs=%v",
			report.HasInlinePTX, report.HasWarpAssumptions, report.HasProprietaryLibs)
	}
}

func TestTranslateCUDAToHIP_RecordsWarningWhenHipifyClangFails(t *testing.T) {
	tmpDir := t.TempDir()
	fakeExe := filepath.Join(tmpDir, "hipify-clang")
	content := "#!/bin/sh\necho 'simulated failure' >&2\nexit 1\n"
	if runtime.GOOS == "windows" {
		fakeExe = filepath.Join(tmpDir, "hipify-clang.bat")
		content = "@echo off\r\necho simulated failure 1>&2\r\nexit /b 1\r\n"
	}
	if err := os.WriteFile(fakeExe, []byte(content), 0755); err != nil {
		t.Fatalf("failed to write fake hipify binary: %v", err)
	}
	t.Setenv("HIPIFY_CLANG_PATH", fakeExe)

	res := TranslateCUDAToHIP("__global__ void k(float *a) { cudaDeviceSynchronize(); }")
	if !res.Success {
		t.Fatalf("expected builtin fallback translation to succeed, got error: %s", res.Error)
	}
	if res.Engine != "builtin-fallback" {
		t.Errorf("expected builtin-fallback engine, got %q", res.Engine)
	}
	if res.Warning == "" {
		t.Errorf("expected non-empty Warning when hipify-clang fails")
	}
}

func TestFindHipifyClang_HonorsEnvPath(t *testing.T) {
	tmpDir := t.TempDir()
	customExe := filepath.Join(tmpDir, "custom-hipify-clang")
	if runtime.GOOS == "windows" {
		customExe += ".exe"
	}
	if err := os.WriteFile(customExe, []byte("stub"), 0755); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	t.Setenv("HIPIFY_CLANG_PATH", customExe)
	if got := FindHipifyClang(); got != customExe {
		t.Fatalf("FindHipifyClang() = %q, want %q from HIPIFY_CLANG_PATH", got, customExe)
	}

	// Directory path in HIPIFY_CLANG_PATH must NOT be returned
	t.Setenv("HIPIFY_CLANG_PATH", tmpDir)
	if got := FindHipifyClang(); got == tmpDir {
		t.Fatalf("FindHipifyClang() should ignore directory path %q", tmpDir)
	}
}

