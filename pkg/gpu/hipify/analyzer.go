package hipify

import (
	"bufio"
	"regexp"
	"strings"
)

// IssueSeverity indicates how severely an incompatibility affects cross-vendor execution.
type IssueSeverity string

const (
	SeverityError   IssueSeverity = "ERROR"   // Kernel cannot execute correctly on AMD
	SeverityWarning IssueSeverity = "WARNING" // Potential performance or behavior difference
	SeverityInfo    IssueSeverity = "INFO"    // Informational translation note
)

// SuggestedAction recommends the next step for the task capsule.
type SuggestedAction string

const (
	ActionExecuteNVIDIA         SuggestedAction = "EXECUTE_NVIDIA_ONLY"
	ActionTranslateHIP          SuggestedAction = "TRANSLATE_TO_HIP"
	ActionRejectRecommendingONNX SuggestedAction = "REJECT_RECOMMEND_ONNX"
)

// CompatibilityIssue describes a detected non-portable pattern in the CUDA kernel.
type CompatibilityIssue struct {
	LineNumber  int           `json:"line_number"`
	Construct   string        `json:"construct"`
	Severity    IssueSeverity `json:"severity"`
	Reason      string        `json:"reason"`
	Remediation string        `json:"remediation"`
}

// CompatibilityReport summarizes the portability analysis of a CUDA C++ source file.
type CompatibilityReport struct {
	IsPortable          bool                 `json:"is_portable"`
	TargetAMDSupported  bool                 `json:"target_amd_supported"`
	SuggestedAction     SuggestedAction      `json:"suggested_action"`
	TotalLines          int                  `json:"total_lines"`
	Issues              []CompatibilityIssue `json:"issues"`
	HasWarpAssumptions  bool                 `json:"has_warp_assumptions"`
	HasInlinePTX        bool                 `json:"has_inline_ptx"`
	HasProprietaryLibs  bool                 `json:"has_proprietary_libs"`
	HasDynamicLaunch    bool                 `json:"has_dynamic_launch"`
}

var (
	// Warp-level 32-thread intrinsics and patterns
	reWarpSync = regexp.MustCompile(`\b(__shfl_sync|__shfl_up_sync|__shfl_down_sync|__shfl_xor_sync|__any_sync|__all_sync|__ballot_sync|__activemask)\b`)
	reWarpMod  = regexp.MustCompile(`threadIdx\.[xyz]\s*%\s*32`)

	// Inline PTX assembly
	reInlinePTX = regexp.MustCompile(`\b(asm\s+volatile\b|asm\s*\()`)

	// Proprietary CUDA libraries
	reProprietaryLib = regexp.MustCompile(`\b(cudnn\.h|cublas_v2\.h|nvjpeg\.h|tensorrt|nvinfer\.h|curand_kernel\.h)\b`)

	// Kernel launch syntax (used to detect dynamic parallelism inside device code)
	reKernelLaunch = regexp.MustCompile(`<<<\s*[^>]+,\s*[^>]+>>>`)
)

// stripCommentsForLine removes // line comments and /* ... */ block comments from a line
// while preserving multi-line block comment state across lines and ignoring comment markers inside string literals.
func stripCommentsForLine(line string, inBlockComment bool) (string, bool) {
	var out strings.Builder
	out.Grow(len(line))
	inString := false
	escaped := false

	for i := 0; i < len(line); i++ {
		if inBlockComment {
			if i+1 < len(line) && line[i] == '*' && line[i+1] == '/' {
				inBlockComment = false
				i++
				out.WriteByte(' ')
			}
			continue
		}

		ch := line[i]
		if inString {
			out.WriteByte(ch)
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}

		if ch == '"' {
			inString = true
			out.WriteByte(ch)
			continue
		}

		if i+1 < len(line) && ch == '/' && line[i+1] == '/' {
			break
		}
		if i+1 < len(line) && ch == '/' && line[i+1] == '*' {
			inBlockComment = true
			i++
			out.WriteByte(' ')
			continue
		}

		out.WriteByte(ch)
	}

	return out.String(), inBlockComment
}

// AnalyzeCUDASource performs static analysis on CUDA C++ code to check portability to AMD GPUs.
func AnalyzeCUDASource(source string) CompatibilityReport {
	report := CompatibilityReport{
		IsPortable:         true,
		TargetAMDSupported: true,
		SuggestedAction:    ActionTranslateHIP,
		Issues:             make([]CompatibilityIssue, 0),
	}

	scanner := bufio.NewScanner(strings.NewReader(source))
	lineNum := 0
	inBlockComment := false
	inDeviceFunc := false
	deviceBraceDepth := 0
	deviceBraceSeen := false

	for scanner.Scan() {
		lineNum++
		rawLine := scanner.Text()
		line, nextBlockComment := stripCommentsForLine(rawLine, inBlockComment)
		inBlockComment = nextBlockComment
		trimmed := strings.TrimSpace(line)

		// Skip empty/comment-only lines
		if trimmed == "" {
			continue
		}

		// Track device / global function contexts
		if strings.Contains(line, "__device__") || strings.Contains(line, "__global__") {
			inDeviceFunc = true
			deviceBraceSeen = false
			deviceBraceDepth = 0
		}

		if inDeviceFunc {
			openCount := strings.Count(line, "{")
			closeCount := strings.Count(line, "}")
			if openCount > 0 {
				deviceBraceSeen = true
			}
			deviceBraceDepth += openCount - closeCount
		}

		// 1. Check for Warp-level intrinsics (Critical blocker on AMD 64-thread wavefronts)
		if match := reWarpSync.FindString(line); match != "" {
			report.HasWarpAssumptions = true
			report.TargetAMDSupported = false
			report.Issues = append(report.Issues, CompatibilityIssue{
				LineNumber:  lineNum,
				Construct:   match,
				Severity:    SeverityError,
				Reason:      "NVIDIA 32-thread warp synchronous intrinsic does not map directly to AMD 64-thread wavefront execution.",
				Remediation: "Use vendor-neutral ONNX Runtime graph export or rewrite with cooperative_groups / __ballot64.",
			})
		}

		if match := reWarpMod.FindString(line); match != "" {
			report.HasWarpAssumptions = true
			report.TargetAMDSupported = false
			report.Issues = append(report.Issues, CompatibilityIssue{
				LineNumber:  lineNum,
				Construct:   match,
				Severity:    SeverityError,
				Reason:      "Hardcoded modulo 32 indexing assumes 32-thread warp size, causing out-of-bounds or race conditions on 64-thread wavefronts.",
				Remediation: "Index using warpSize variable or export model to portable ONNX capsule.",
			})
		}

		// 2. Check for Inline PTX assembly
		if match := reInlinePTX.FindString(line); match != "" {
			report.HasInlinePTX = true
			report.TargetAMDSupported = false
			report.Issues = append(report.Issues, CompatibilityIssue{
				LineNumber:  lineNum,
				Construct:   match,
				Severity:    SeverityError,
				Reason:      "NVIDIA Parallel Thread Execution (PTX) assembly cannot be compiled by AMD ROCm/HIP compiler.",
				Remediation: "Replace inline PTX with portable C++ math intrinsics or export to ONNX.",
			})
		}

		// 3. Check for Proprietary CUDA closed-source headers
		if match := reProprietaryLib.FindString(line); match != "" {
			report.HasProprietaryLibs = true
			report.TargetAMDSupported = false
			report.Issues = append(report.Issues, CompatibilityIssue{
				LineNumber:  lineNum,
				Construct:   match,
				Severity:    SeverityError,
				Reason:      "Proprietary NVIDIA library header cannot be linked on AMD or non-CUDA systems.",
				Remediation: "Use rocBLAS / MIOpen or export computation to ONNX DirectML capsule.",
			})
		}

		// 4. Check for Dynamic Parallelism
		if inDeviceFunc && reKernelLaunch.MatchString(line) {
			report.HasDynamicLaunch = true
			report.Issues = append(report.Issues, CompatibilityIssue{
				LineNumber:  lineNum,
				Construct:   "<<<...>>>",
				Severity:    SeverityWarning,
				Reason:      "Dynamic parallelism (kernel launching kernel) has vendor-specific runtime overhead on ROCm.",
				Remediation: "Launch child kernels from host or flatten execution into single multi-pass grid.",
			})
		}

		if inDeviceFunc && deviceBraceSeen && deviceBraceDepth <= 0 {
			inDeviceFunc = false
			deviceBraceSeen = false
			deviceBraceDepth = 0
		}
	}

	report.TotalLines = lineNum

	if len(report.Issues) > 0 {
		report.IsPortable = false
		for _, issue := range report.Issues {
			if issue.Severity == SeverityError {
				report.TargetAMDSupported = false
				report.SuggestedAction = ActionRejectRecommendingONNX
				break
			}
		}
	}

	if !report.TargetAMDSupported {
		if !report.HasInlinePTX && !report.HasProprietaryLibs && report.HasWarpAssumptions {
			report.SuggestedAction = ActionExecuteNVIDIA
		}
	}

	return report
}
