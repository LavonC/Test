package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/unicompute/obj1/pkg/codeexec"
	"github.com/unicompute/obj1/pkg/gpu/hipify"
	"github.com/unicompute/obj1/pkg/gpu/onnx"
)

func main() {
	fmt.Println("=======================================================================")
	fmt.Println(" UniCompute Standalone Samples Verification Runner")
	fmt.Println("=======================================================================")

	allPassed := true

	// 1. Test GPU Samples
	fmt.Println("\n[TEST GROUP 1] GPU Samples & HIPIFY Analysis/Translation")

	// 1.1 Portable Vector Add
	portablePath := filepath.Join("samples", "gpu", "portable_vector_add.cu")
	content, err := os.ReadFile(portablePath)
	if err != nil {
		fmt.Printf(" [FAIL] Could not read %s: %v\n", portablePath, err)
		allPassed = false
	} else {
		res := hipify.TranslateCUDAToHIP(string(content))
		if !res.Success || !res.Report.IsPortable || res.Report.SuggestedAction != "TRANSLATE_TO_HIP" {
			fmt.Printf(" [FAIL] portable_vector_add.cu failed: Success=%v, Action=%s\n", res.Success, res.Report.SuggestedAction)
			allPassed = false
		} else {
			fmt.Printf("  -> [PASS] %s: Translated to HIP C++ (%d replacements, Action=%s)\n",
				portablePath, res.ReplacementsMade, res.Report.SuggestedAction)
		}
	}

	// 1.2 Warp Reduction Blocker
	warpPath := filepath.Join("samples", "gpu", "warp_reduction.cu")
	content, err = os.ReadFile(warpPath)
	if err != nil {
		fmt.Printf(" [FAIL] Could not read %s: %v\n", warpPath, err)
		allPassed = false
	} else {
		res := hipify.TranslateCUDAToHIP(string(content))
		if res.Success || res.Report.IsPortable || res.Report.SuggestedAction != "EXECUTE_NVIDIA_ONLY" {
			fmt.Printf(" [FAIL] warp_reduction.cu should be blocked: Success=%v, Action=%s\n", res.Success, res.Report.SuggestedAction)
			allPassed = false
		} else {
			fmt.Printf("  -> [PASS] %s: Blocked on 32-thread warp assumptions (%d issues, Action=%s)\n",
				warpPath, len(res.Report.Issues), res.Report.SuggestedAction)
			for _, issue := range res.Report.Issues {
				fmt.Printf("     * Line %d: [%s] %s\n", issue.LineNumber, issue.Construct, issue.Reason)
			}
		}
	}

	// 1.3 Inline PTX Blocker
	ptxPath := filepath.Join("samples", "gpu", "inline_ptx.cu")
	content, err = os.ReadFile(ptxPath)
	if err != nil {
		fmt.Printf(" [FAIL] Could not read %s: %v\n", ptxPath, err)
		allPassed = false
	} else {
		res := hipify.TranslateCUDAToHIP(string(content))
		if res.Success || res.Report.IsPortable || res.Report.SuggestedAction != "REJECT_RECOMMEND_ONNX" {
			fmt.Printf(" [FAIL] inline_ptx.cu should be blocked: Success=%v, Action=%s\n", res.Success, res.Report.SuggestedAction)
			allPassed = false
		} else {
			fmt.Printf("  -> [PASS] %s: Blocked on inline PTX assembly (%d issues, Action=%s)\n",
				ptxPath, len(res.Report.Issues), res.Report.SuggestedAction)
			for _, issue := range res.Report.Issues {
				fmt.Printf("     * Line %d: [%s] %s\n", issue.LineNumber, issue.Construct, issue.Reason)
			}
		}
	}

	// 1.4 Test Model JSON Capsule & Checkpoint
	fmt.Println("\n[TEST GROUP 2] Model Capsule & Checkpoint Serialization")
	modelPath := filepath.Join("samples", "gpu", "test_model.json")
	modelBytes, err := os.ReadFile(modelPath)
	if err != nil {
		fmt.Printf(" [FAIL] Could not read %s: %v\n", modelPath, err)
		allPassed = false
	} else {
		var modelMap map[string]any
		if err := json.Unmarshal(modelBytes, &modelMap); err != nil {
			fmt.Printf(" [FAIL] Could not parse %s as JSON: %v\n", modelPath, err)
			allPassed = false
		} else {
			fmt.Printf("  -> [PASS] %s: Valid JSON capsule parsed. Model: %v, Layer: %v, InputShape: %v\n",
				modelPath, modelMap["model_name"], modelMap["layer_type"], modelMap["input_shape"])

			// Test saving and loading a checkpoint
			tmpDir, err := os.MkdirTemp("", "sample-onnx-test-*")
			if err != nil {
				fmt.Printf(" [FAIL] Could not create temp dir: %v\n", err)
				allPassed = false
			} else {
				defer os.RemoveAll(tmpDir)
				ckptPath := filepath.Join(tmpDir, "sample_checkpoint.ckpt")
				ckpt := onnx.Checkpoint{
					JobID:        "test-job-sample",
					Step:         10,
					Loss:         0.42,
					LearningRate: 0.001,
					Weights: map[string][]float32{
						"w1": {0.1, 0.2, -0.3, 0.4},
						"b1": {0.01, 0.02},
					},
				}
				hashHex, err := onnx.SaveCheckpoint(ckpt, ckptPath)
				if err != nil {
					fmt.Printf(" [FAIL] SaveCheckpoint failed: %v\n", err)
					allPassed = false
				} else {
					fmt.Printf("  -> [PASS] Checkpoint saved: Step=%d, SHA256=%s\n", ckpt.Step, hashHex[:16]+"...")

					loaded, err := onnx.LoadCheckpoint(ckptPath)
					if err != nil || loaded.SHA256Checksum != hashHex {
						fmt.Printf(" [FAIL] LoadCheckpoint failed or hash mismatch: %v\n", err)
						allPassed = false
					} else {
						fmt.Println("  -> [PASS] Checkpoint integrity verified successfully.")
					}
				}
			}
		}
	}

	// 2. Test Code Execution Protocol Encapsulation on Objective 2 Samples
	fmt.Println("\n[TEST GROUP 3] Container Code Execution Protocol Encapsulation")
	codeSamples := []struct {
		name string
		lang string
		path string
	}{
		{"calc.py", "python", filepath.Join("samples", "python", "calc.py")},
		{"hello.c", "c", filepath.Join("samples", "c", "hello.c")},
		{"fib.cpp", "cpp", filepath.Join("samples", "cpp", "fib.cpp")},
		{"timeout_test.py", "python", filepath.Join("samples", "security", "timeout_test.py")},
		{"network_leak_test.py", "python", filepath.Join("samples", "security", "network_leak_test.py")},
		{"mem_limit_test.py", "python", filepath.Join("samples", "security", "mem_limit_test.py")},
	}

	for _, sample := range codeSamples {
		src, err := os.ReadFile(sample.path)
		if err != nil {
			fmt.Printf(" [FAIL] Could not read %s: %v\n", sample.path, err)
			allPassed = false
			continue
		}

		req := codeexec.Request{
			JobID:          "verify-" + sample.name,
			Language:       sample.lang,
			Source:         string(src),
			TimeoutSeconds: 10,
		}

		encoded, err := codeexec.EncodeRequest(req)
		if err != nil {
			fmt.Printf(" [FAIL] EncodeRequest failed for %s: %v\n", sample.name, err)
			allPassed = false
			continue
		}

		decoded, err := codeexec.DecodeRequest(encoded)
		if err != nil {
			fmt.Printf(" [FAIL] DecodeRequest failed for %s: %v\n", sample.name, err)
			allPassed = false
			continue
		}

		if decoded.JobID != req.JobID || decoded.Language != strings.ToLower(req.Language) || len(decoded.Source) != len(req.Source) {
			fmt.Printf(" [FAIL] Round-trip mismatch for %s\n", sample.name)
			allPassed = false
			continue
		}

		fmt.Printf("  -> [PASS] %s (%s, %d bytes): Wire protocol encode/decode verified.\n",
			sample.name, sample.lang, len(src))
	}

	fmt.Println("\n=======================================================================")
	if allPassed {
		fmt.Println(" [SUCCESS] All standalone sample verifications passed!")
	} else {
		fmt.Println(" [FAILURE] One or more standalone sample verifications failed.")
		os.Exit(1)
	}
	fmt.Println("=======================================================================")
}
