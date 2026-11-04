//go:build !windows

package gpu

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ProbeHardware discovers GPU adapters, VRAM, and supported compute backends on Linux/Unix.
func ProbeHardware(ctx context.Context) (GPUProfile, error) {
	profile := GPUProfile{
		Vendor:            VendorUnknown,
		DeviceName:        "Generic Compute Device",
		SupportedBackends: []ExecutionBackend{BackendCPU},
		Timestamp:         time.Now(),
	}

	// 1. Check for NVIDIA via nvidia-smi
	smiCmd := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=name,memory.total,memory.free,driver_version,temperature.gpu", "--format=csv,noheader,nounits")
	smiOut, err := smiCmd.Output()
	if err == nil {
		parts := strings.Split(strings.TrimSpace(string(smiOut)), ",")
		if len(parts) >= 4 {
			profile.Vendor = VendorNVIDIA
			profile.DeviceName = strings.TrimSpace(parts[0])
			totalMB, _ := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64)
			freeMB, _ := strconv.ParseUint(strings.TrimSpace(parts[2]), 10, 64)
			profile.TotalVRAMBytes = totalMB * 1024 * 1024
			profile.AvailableVRAMBytes = freeMB * 1024 * 1024
			profile.DriverVersion = strings.TrimSpace(parts[3])
			profile.CUDASupported = true
			profile.SupportedBackends = append(profile.SupportedBackends, BackendCUDA)

			if len(parts) >= 5 {
				temp, _ := strconv.ParseFloat(strings.TrimSpace(parts[4]), 64)
				profile.TemperatureC = temp
			}
			return profile, nil
		}
	}

	// 2. Check for AMD via rocm-smi
	rocmCmd := exec.CommandContext(ctx, "rocm-smi", "--showproductname", "--showdriverversion")
	rocmOut, rErr := rocmCmd.Output()
	if rErr == nil {
		profile.Vendor = VendorAMD
		profile.DeviceName = "AMD ROCm Compatible Device"
		profile.SupportedBackends = append(profile.SupportedBackends, BackendROCm)
		if strings.Contains(string(rocmOut), "Radeon") {
			profile.DeviceName = "AMD Radeon GPU"
		}
		return profile, nil
	}

	return profile, nil
}
