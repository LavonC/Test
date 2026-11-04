//go:build windows

package gpu

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")
)

// ProbeHardware discovers GPU adapters, VRAM, and supported compute backends on Windows.
func ProbeHardware(ctx context.Context) (GPUProfile, error) {
	profile := GPUProfile{
		Vendor:            VendorUnknown,
		DeviceName:        "Unknown Graphics Device",
		SupportedBackends: []ExecutionBackend{BackendCPU},
		Timestamp:         time.Now(),
	}

	// 1. Detect DirectML support in Windows (directml.dll in System32)
	dmlDLL, err := syscall.LoadLibrary("directml.dll")
	if err == nil {
		profile.DirectMLSupported = true
		profile.SupportedBackends = append(profile.SupportedBackends, BackendDirectML)
		_ = syscall.FreeLibrary(dmlDLL)
	}

	// 2. Detect CUDA support in Windows (nvcuda.dll in System32)
	cudaDLL, err := syscall.LoadLibrary("nvcuda.dll")
	if err == nil {
		profile.CUDASupported = true
		profile.SupportedBackends = append(profile.SupportedBackends, BackendCUDA)
		_ = syscall.FreeLibrary(cudaDLL)
	}

	// 3. Query adapter details using PowerShell CIM query (fast, safe across Win 10/11)
	psCmd := `Get-CimInstance Win32_VideoController | ForEach-Object { "$($_.Name)|$($_.AdapterRAM)|$($_.DriverVersion)|$($_.PNPDeviceID)" }`
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", psCmd)
	out, err := cmd.Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		// Find the best discrete/dedicated GPU, prioritizing NVIDIA or AMD over integrated Intel
		var bestScore int
		for _, rawLine := range lines {
			line := strings.TrimSpace(rawLine)
			if line == "" {
				continue
			}
			parts := strings.Split(line, "|")
			if len(parts) < 4 {
				continue
			}
			name := strings.TrimSpace(parts[0])
			ramBytes, _ := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64)
			driverVer := strings.TrimSpace(parts[2])
			pnpID := strings.ToUpper(strings.TrimSpace(parts[3]))

			vendor := VendorUnknown
			score := 1
			if strings.Contains(pnpID, "VEN_10DE") || strings.Contains(strings.ToUpper(name), "NVIDIA") {
				vendor = VendorNVIDIA
				score = 10
			} else if strings.Contains(pnpID, "VEN_1002") || strings.Contains(strings.ToUpper(name), "AMD") || strings.Contains(strings.ToUpper(name), "RADEON") {
				vendor = VendorAMD
				score = 9
			} else if strings.Contains(pnpID, "VEN_8086") || strings.Contains(strings.ToUpper(name), "INTEL") {
				vendor = VendorIntel
				score = 5
			}

			// Prioritize discrete GPUs or highest score
			if score > bestScore || (score == bestScore && ramBytes > profile.TotalVRAMBytes) {
				bestScore = score
				profile.Vendor = vendor
				profile.DeviceName = name
				profile.TotalVRAMBytes = ramBytes
				profile.DriverVersion = driverVer
			}
		}
	}

	// If AdapterRAM is 0 or reported as a 32-bit clamped value (common in 32-bit WMI for >4GB VRAM),
	// probe via nvidia-smi if NVIDIA, or provide realistic baseline.
	if profile.Vendor == VendorNVIDIA {
		smiCmd := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=memory.total,memory.free,temperature.gpu,power.draw", "--format=csv,noheader,nounits")
		smiOut, smiErr := smiCmd.Output()
		if smiErr == nil {
			smiParts := strings.Split(strings.TrimSpace(string(smiOut)), ",")
			if len(smiParts) >= 2 {
				totalMB, _ := strconv.ParseUint(strings.TrimSpace(smiParts[0]), 10, 64)
				freeMB, _ := strconv.ParseUint(strings.TrimSpace(smiParts[1]), 10, 64)
				if totalMB > 0 {
					profile.TotalVRAMBytes = totalMB * 1024 * 1024
					profile.AvailableVRAMBytes = freeMB * 1024 * 1024
				}
				if len(smiParts) >= 3 {
					temp, _ := strconv.ParseFloat(strings.TrimSpace(smiParts[2]), 64)
					profile.TemperatureC = temp
				}
				if len(smiParts) >= 4 {
					power, _ := strconv.ParseFloat(strings.TrimSpace(smiParts[3]), 64)
					profile.PowerUsageW = power
				}
			}
		}
	}

	if profile.AvailableVRAMBytes == 0 && profile.TotalVRAMBytes > 0 {
		// Conservative estimate if real-time query isn't available
		profile.AvailableVRAMBytes = (profile.TotalVRAMBytes * 80) / 100
	}

	return profile, nil
}
