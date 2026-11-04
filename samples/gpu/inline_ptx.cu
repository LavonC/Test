#include <cuda_runtime.h>

__device__ unsigned int getSpecialRegister() {
    unsigned int reg;
    // Inline PTX assembly (cannot compile under ROCm/HIP)
    asm volatile("mov.u32 %0, %smid;" : "=r"(reg));
    return reg;
}

__global__ void ptxKernel(unsigned int *out) {
    if (threadIdx.x == 0) {
        out[blockIdx.x] = getSpecialRegister();
    }
}
