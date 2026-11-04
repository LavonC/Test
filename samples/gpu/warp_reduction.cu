#include <cuda_runtime.h>

__global__ void warpReduction(float *d_out, float *d_in) {
    int lane = threadIdx.x % 32; // Assuming 32-thread warp
    float val = d_in[threadIdx.x];

    // Warp-level shuffle intrinsic (fails on AMD 64-thread wavefront)
    for (int offset = 16; offset > 0; offset /= 2) {
        val += __shfl_down_sync(0xffffffff, val, offset);
    }

    if (lane == 0) {
        d_out[blockIdx.x] = val;
    }
}
