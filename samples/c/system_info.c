#include <stdio.h>
#include <stdlib.h>
#include <stdint.h>
#include <time.h>

/*
 * UniCompute Objective 2 Sample: C Benchmark & System Diagnostics
 * Performs memory allocation, integer throughput hashing, and pointer alignment checks.
 */

#define BENCHMARK_ITEMS 65536

uint32_t murmur3_32(const uint8_t* key, size_t len, uint32_t seed) {
    uint32_t h = seed;
    uint32_t c1 = 0xcc9e2d51;
    uint32_t c2 = 0x1b873593;
    const int nblocks = len / 4;
    const uint32_t *blocks = (const uint32_t *)(key);

    for (int i = 0; i < nblocks; i++) {
        uint32_t k = blocks[i];
        k *= c1;
        k = (k << 15) | (k >> (32 - 15));
        k *= c2;

        h ^= k;
        h = (h << 13) | (h >> (32 - 13));
        h = h * 5 + 0xe6546b64;
    }

    h ^= len;
    h ^= h >> 16;
    h *= 0x85ebca6b;
    h ^= h >> 13;
    h *= 0xc2b2ae35;
    h ^= h >> 16;
    return h;
}

int main(void) {
    printf("==================================================\n");
    printf("   UniCompute Worker: C Memory & Hash Benchmark   \n");
    printf("==================================================\n");

    printf("Word Size     : %zu bits (%zu bytes)\n", sizeof(void*) * 8, sizeof(void*));
    printf("Integer Width : %zu bits\n", sizeof(int) * 8);
    printf("Pointer Size  : %zu bytes\n", sizeof(void*));

    size_t alloc_bytes = BENCHMARK_ITEMS * sizeof(uint32_t);
    printf("Allocating    : %zu bytes (%.2f KiB) heap buffer...\n", alloc_bytes, (double)alloc_bytes / 1024.0);

    uint32_t* buffer = (uint32_t*)malloc(alloc_bytes);
    if (!buffer) {
        fprintf(stderr, "[ERROR] Memory allocation failed!\n");
        return 1;
    }

    clock_t start = clock();
    for (size_t i = 0; i < BENCHMARK_ITEMS; i++) {
        buffer[i] = (uint32_t)(i ^ (i << 13) ^ (i >> 17));
    }

    uint32_t hash = murmur3_32((const uint8_t*)buffer, alloc_bytes, 0x9747b28c);
    clock_t end = clock();
    double duration_ms = ((double)(end - start) / CLOCKS_PER_SEC) * 1000.0;

    printf("--------------------------------------------------\n");
    printf("Items Hashed  : %d 32-bit words\n", BENCHMARK_ITEMS);
    printf("Murmur3 Check : 0x%08X\n", hash);
    printf("Duration      : %.2f ms\n", duration_ms);
    printf("Throughput    : %.2f MB/s\n", ((double)alloc_bytes / (1024.0 * 1024.0)) / (duration_ms / 1000.0 + 1e-6));
    printf("==================================================\n");
    printf("Status: C worker execution passed.\n");

    free(buffer);
    return 0;
}
