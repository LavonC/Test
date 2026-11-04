package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/unicompute/obj1/pkg/codeexec"
	"github.com/unicompute/obj1/pkg/gpu"
	"github.com/unicompute/obj1/pkg/node"
)

//go:embed web/*
var embeddedWebFS embed.FS

func main() {
	nodeID := flag.String("id", "", "Unique node identifier (default: generated UUID)")
	poolID := flag.String("pool", "default-pool", "Storage pool identifier")
	storageDir := flag.String("storage", "", "Storage directory for chunk blobs")
	quotaGB := flag.Int64("quota-gb", 10, "Contributed storage quota in GiB")
	signalingURL := flag.String("signaling", "wss://unicompute-coordinator.onrender.com/ws", "Coordinator signaling WebSocket URL")
	stunServer := flag.String("stun", "stun.l.google.com:19302", "STUN server address")
	stunServer6 := flag.String("stun6", "", "IPv6 STUN server address")
	enableGPU := flag.Bool("enable-gpu", true, "Enable Objective 3 GPU acceleration and resource governance")
	enableCode := flag.Bool("enable-code-execution", true, "Enable Docker sandbox code execution if available")
	codeAllowlist := flag.String("code-allowlist", "*", "Comma-separated peer IDs allowed to submit code jobs (* for any)")
	headless := flag.Bool("headless", false, "Run in headless server mode without launching desktop window")
	port := flag.Int("port", 23080, "Port for GUI web server")
	listenHost := flag.String("listen", "127.0.0.1", "Host interface to bind the GUI server to (use 0.0.0.0 for LAN)")

	flag.Parse()

	if *nodeID == "" {
		*nodeID = "node-" + uuid.New().String()[:8]
	}

	if *storageDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		*storageDir = filepath.Join(home, ".unicompute", "storage", *nodeID)
	}

	var stunListV4 []string
	if *stunServer != "" {
		for _, s := range strings.Split(*stunServer, ",") {
			if s = strings.TrimSpace(s); s != "" {
				stunListV4 = append(stunListV4, s)
			}
		}
	}

	var stunListV6 []string
	if *stunServer6 != "" {
		for _, s := range strings.Split(*stunServer6, ",") {
			if s = strings.TrimSpace(s); s != "" {
				stunListV6 = append(stunListV6, s)
			}
		}
	}

	quotaBytes := *quotaGB * 1024 * 1024 * 1024
	nodeCfg := node.NodeConfig{
		NodeID:          *nodeID,
		PoolID:          *poolID,
		StorageDir:      *storageDir,
		StorageQuota:    quotaBytes,
		SignalingURL:    *signalingURL,
		STUNServersV4:   stunListV4,
		STUNServersV6:   stunListV6,
		FallbackTimeout: 2000 * time.Millisecond,
	}

	n, err := node.NewNode(nodeCfg)
	if err != nil {
		log.Fatalf("[FATAL] Failed to initialize UniCompute node: %v\n", err)
	}
	defer n.Close()

	// Initialize GPU Subsystem (Objective 3)
	gpuCfg := gpu.ManagerConfig{
		StorageDir:          filepath.Join(*storageDir, "gpu"),
		MaxMemoryBytes:      2048 * 1024 * 1024,
		PreferredBackend:    gpu.BackendDirectML,
		WatchdogInterval:    50 * time.Millisecond,
		UserActiveThreshold: 1000 * time.Millisecond,
		ResumeGracePeriod:   30 * time.Second,
	}

	if *enableGPU {
		if err := n.EnableGPU(gpuCfg); err != nil {
			log.Printf("[WARN] Failed to enable GPU subsystem: %v\n", err)
		} else {
			prof, _ := n.GPUProfile()
			log.Printf("[GPU] Objective 3 enabled: %s (%s, %d MB VRAM, DirectML=%v, CUDA=%v)",
				prof.DeviceName, prof.Vendor, prof.TotalVRAMBytes/(1024*1024), prof.DirectMLSupported, prof.CUDASupported)
		}
	}

	// Initialize Docker Sandbox (Objective 2) if Docker is present
	if *enableCode {
		dockerCtx, cancelDocker := context.WithTimeout(context.Background(), 3*time.Second)
		if err := codeexec.CheckDocker(dockerCtx); err == nil {
			var allowedPeers []string
			if *codeAllowlist != "" && *codeAllowlist != "*" {
				allowedPeers = strings.Split(*codeAllowlist, ",")
			}
			n.SetCodeExecutor(codeexec.NewDockerExecutor(codeexec.Config{
				DefaultTimeout: 10 * time.Second,
				MaxTimeout:     30 * time.Second,
				MaxConcurrent:  2,
			}), allowedPeers...)
			log.Println("[CODEEXEC] Tier 2 Docker sandbox active on worker")
		} else {
			log.Printf("[CODEEXEC] Docker not available (%v); container jobs will be forwarded to peers\n", err)
		}
		cancelDocker()
	}

	ctx, cancelNode := context.WithCancel(context.Background())
	defer cancelNode()

	log.Printf("[INFO] Starting UniCompute node '%s' on %s...\n", *nodeID, *signalingURL)
	if err := n.Start(ctx); err != nil {
		log.Printf("[WARN] Node startup note: %v (operating in local/direct mode)\n", err)
	}

	// Initialize GUI Server State & API Mux
	state := NewServerState(n, nodeCfg, *enableGPU, gpuCfg)
	mux := http.NewServeMux()
	state.SetupRoutes(mux)

	// Serve embedded web frontend assets
	subFS, err := fs.Sub(embeddedWebFS, "web")
	if err != nil {
		log.Fatalf("[FATAL] Failed to access embedded web files: %v\n", err)
	}
	mux.Handle("/", http.FileServer(http.FS(subFS)))

	// Find free port if default is occupied
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", *listenHost, *port))
	if err != nil {
		// Fallback to random free port
		listener, err = net.Listen("tcp", fmt.Sprintf("%s:0", *listenHost))
		if err != nil {
			log.Fatalf("[FATAL] Failed to bind local GUI server: %v\n", err)
		}
	}
	actualPort := listener.Addr().(*net.TCPAddr).Port
	guiURL := fmt.Sprintf("http://127.0.0.1:%d", actualPort)

	srv := &http.Server{Handler: mux}
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("[ERROR] GUI server error: %v\n", err)
		}
	}()

	fmt.Println("=========================================================================")
	fmt.Println(" UniCompute Native Desktop Application Active")
	fmt.Printf(" Node ID:        %s\n", *nodeID)
	fmt.Printf(" Coordinator:    %s\n", *signalingURL)
	fmt.Printf(" Local GUI URL:  %s\n", guiURL)
	fmt.Println("=========================================================================")

	// Launch dedicated native desktop app window unless headless
	if !*headless {
		go func() {
			time.Sleep(300 * time.Millisecond)
			log.Printf("[GUI] Launching native desktop window for %s...\n", guiURL)
			cmd, err := launchDesktopWindow(guiURL)
			if err != nil {
				log.Printf("[WARN] Could not spawn desktop window (%v). Access GUI via: %s\n", err, guiURL)
			} else if cmd != nil {
				_ = cmd.Wait() // When user closes desktop window, trigger graceful exit
				log.Println("[GUI] Desktop window closed by user. Exiting daemon...")
				p, _ := os.FindProcess(os.Getpid())
				_ = p.Signal(os.Interrupt)
			}
		}()
	} else {
		log.Printf("[HEADLESS] Running in headless mode. Open %s in your browser.\n", guiURL)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	log.Println("[INFO] Shutting down UniCompute GUI gracefully...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
}
