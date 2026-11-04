package hipify

import (
	"fmt"
	"strings"
)

// TranslationResult contains the translated HIP source or diagnostics if translation fails.
type TranslationResult struct {
	Success          bool                `json:"success"`
	TranslatedSource string              `json:"translated_source,omitempty"`
	Report           CompatibilityReport `json:"report"`
	ReplacementsMade int                 `json:"replacements_made"`
	Error            string              `json:"error,omitempty"`
}

// Simple map of standard CUDA API symbols to HIP equivalents
var apiReplacements = [][2]string{
	{"#include <cuda_runtime.h>", "#include <hip/hip_runtime.h>"},
	{"#include <cuda.h>", "#include <hip/hip_runtime.h>"},
	{"cudaMalloc", "hipMalloc"},
	{"cudaFree", "hipFree"},
	{"cudaMemcpy", "hipMemcpy"},
	{"cudaMemcpyHostToDevice", "hipMemcpyHostToDevice"},
	{"cudaMemcpyDeviceToHost", "hipMemcpyDeviceToHost"},
	{"cudaMemcpyDeviceToDevice", "hipMemcpyDeviceToDevice"},
	{"cudaMemset", "hipMemset"},
	{"cudaDeviceSynchronize", "hipDeviceSynchronize"},
	{"cudaGetLastError", "hipGetLastError"},
	{"cudaGetErrorString", "hipGetErrorString"},
	{"cudaSuccess", "hipSuccess"},
	{"cudaError_t", "hipError_t"},
	{"cudaStream_t", "hipStream_t"},
	{"cudaStreamCreate", "hipStreamCreate"},
	{"cudaStreamDestroy", "hipStreamDestroy"},
	{"cudaStreamSynchronize", "hipStreamSynchronize"},
	{"cudaEvent_t", "hipEvent_t"},
	{"cudaEventCreate", "hipEventCreate"},
	{"cudaEventDestroy", "hipEventDestroy"},
	{"cudaEventRecord", "hipEventRecord"},
	{"cudaEventSynchronize", "hipEventSynchronize"},
	{"cudaEventElapsedTime", "hipEventElapsedTime"},
}

// TranslateCUDAToHIP performs automated source-to-source conversion of valid, portable CUDA C++ kernels into HIP C++.
func TranslateCUDAToHIP(cudaSource string) TranslationResult {
	report := AnalyzeCUDASource(cudaSource)

	// If source contains critical architectural blockers (Warp intrinsics, inline PTX, proprietary headers),
	// do NOT generate broken or mathematically incorrect HIP code.
	if !report.TargetAMDSupported {
		return TranslationResult{
			Success: false,
			Report:  report,
			Error:   fmt.Sprintf("cannot safely translate to HIP: %d fatal compatibility issue(s) detected", len(report.Issues)),
		}
	}

	translated := cudaSource
	totalReplacements := 0

	for _, rep := range apiReplacements {
		count := strings.Count(translated, rep[0])
		if count > 0 {
			translated = strings.ReplaceAll(translated, rep[0], rep[1])
			totalReplacements += count
		}
	}

	// Prepend HIP runtime header if not already present
	if !strings.Contains(translated, "<hip/hip_runtime.h>") {
		translated = "#include <hip/hip_runtime.h>\n" + translated
		totalReplacements++
	}

	return TranslationResult{
		Success:          true,
		TranslatedSource: translated,
		Report:           report,
		ReplacementsMade: totalReplacements,
	}
}
