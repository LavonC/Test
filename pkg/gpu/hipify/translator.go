package hipify

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TranslationResult contains the translated HIP source or diagnostics if translation fails.
type TranslationResult struct {
	Success          bool                `json:"success"`
	TranslatedSource string              `json:"translated_source,omitempty"`
	Report           CompatibilityReport `json:"report"`
	ReplacementsMade int                 `json:"replacements_made"`
	Engine           string              `json:"engine,omitempty"`
	Warning          string              `json:"warning,omitempty"`
	Error            string              `json:"error,omitempty"`
}

var reConvertedCount = regexp.MustCompile(`CONVERTED refs count:\s*(\d+)`)

// Simple map of standard CUDA API symbols to HIP equivalents (used as fallback if hipify-clang.exe is unavailable).
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

// FindHipifyClang locates the hipify-clang executable on the local machine.
func FindHipifyClang() string {
	if envPath := strings.TrimSpace(os.Getenv("HIPIFY_CLANG_PATH")); envPath != "" {
		if info, err := os.Stat(envPath); err == nil && !info.IsDir() {
			return envPath
		}
	}

	for _, name := range []string{"hipify-clang.exe", "hipify-clang"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}

	// Check standard OS/user install locations
	candidates := []string{
		`/opt/rocm/bin/hipify-clang`,
		`/opt/rocm/hip/bin/hipify-clang`,
		`/usr/local/bin/hipify-clang`,
		`C:\Program Files\AMD\ROCm\bin\hipify-clang.exe`,
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates,
			filepath.Join(home, "hipify", "bin", "hipify-clang.exe"),
			filepath.Join(home, "hipify", "bin", "hipify-clang"),
		)
	}

	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	return ""
}

// translateWithHipifyClang runs the local hipify-clang.exe binary on cudaSource.
func translateWithHipifyClang(hipifyExe, cudaSource string) (string, int, error) {
	tmpDir, err := os.MkdirTemp("", "unicompute-hipify-*")
	if err != nil {
		return "", 0, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	inPath := filepath.Join(tmpDir, "kernel.cu")
	outPath := filepath.Join(tmpDir, "kernel.hip")

	if err := os.WriteFile(inPath, []byte(cudaSource), 0644); err != nil {
		return "", 0, fmt.Errorf("write temp cuda file: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, hipifyExe, inPath, "-o", outPath, "--print-stats")
	outBytes, runErr := cmd.CombinedOutput()

	hipBytes, readErr := os.ReadFile(outPath)
	if readErr != nil {
		if runErr != nil {
			return "", 0, fmt.Errorf("hipify-clang failed: %w (%s)", runErr, strings.TrimSpace(string(outBytes)))
		}
		return "", 0, fmt.Errorf("read hipified output: %w", readErr)
	}

	replacements := 0
	if m := reConvertedCount.FindSubmatch(outBytes); len(m) == 2 {
		if n, convErr := strconv.Atoi(string(m[1])); convErr == nil {
			replacements = n
		}
	}

	return string(hipBytes), replacements, nil
}

func translateBuiltin(cudaSource string) (string, int) {
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

	return translated, totalReplacements
}

// TranslateCUDAToHIP performs automated source-to-source conversion of valid, portable CUDA C++ kernels into HIP C++.
// It uses the local hipify-clang.exe when available and falls back to built-in mapping otherwise.
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

	var warning string
	if hipifyExe := FindHipifyClang(); hipifyExe != "" {
		translated, count, err := translateWithHipifyClang(hipifyExe, cudaSource)
		if err == nil {
			return TranslationResult{
				Success:          true,
				TranslatedSource: translated,
				Report:           report,
				ReplacementsMade: count,
				Engine:           filepath.Base(hipifyExe),
			}
		}
		warning = fmt.Sprintf("%s failed (%v); fell back to builtin-fallback", filepath.Base(hipifyExe), err)
	}

	translated, totalReplacements := translateBuiltin(cudaSource)
	return TranslationResult{
		Success:          true,
		TranslatedSource: translated,
		Report:           report,
		ReplacementsMade: totalReplacements,
		Engine:           "builtin-fallback",
		Warning:          warning,
	}
}
