package hipify

import (
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
