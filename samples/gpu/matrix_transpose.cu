/*
 * UniCompute Objective 3 Sample: Shared Memory Matrix Transpose
 * Portable CUDA kernel demonstrating 2D thread block indexing and __syncthreads().
 * Valid for translation via HIPIFY to AMD ROCm / HIP.
 */

#define TILE_DIM 32
#define BLOCK_ROWS 8

__global__ void transposeCoalesced(float *odata, const float *idata, int width, int height) {
    __shared__ float tile[TILE_DIM][TILE_DIM + 1]; // +1 prevents bank conflicts

    int x = blockIdx.x * TILE_DIM + threadIdx.x;
    int y = blockIdx.y * TILE_DIM + threadIdx.y;

    for (int j = 0; j < TILE_DIM; j += BLOCK_ROWS) {
        if (x < width && (y + j) < height) {
            tile[threadIdx.y + j][threadIdx.x] = idata[(y + j) * width + x];
        }
    }

    __syncthreads();

    x = blockIdx.y * TILE_DIM + threadIdx.x;
    y = blockIdx.x * TILE_DIM + threadIdx.y;

    for (int j = 0; j < TILE_DIM; j += BLOCK_ROWS) {
        if (x < height && (y + j) < width) {
            odata[(y + j) * height + x] = tile[threadIdx.x][threadIdx.y + j];
        }
    }
}
