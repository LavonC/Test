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
	authToken := flag.String("auth-token", os.Getenv("UNICOMPUTE_AUTH_TOKEN"), "Coordinator signaling authentication token")
	stunServer := flag.String("stun", "stun.l.google.com:19302", "STUN server address")
	stunServer6 := flag.String("stun6", "", "IPv6 STUN server address")
	enableGPU := flag.Bool("enable-gpu", true, "Enable Objective 3 GPU acceleration and resource governance")
	enableCode := flag.Bool("enable-code-execution", true, "Enable Docker sandbox code execution if available")
	codeAllowlist := flag.String("code-allowlist", "", "Comma-separated peer IDs allowed to submit code jobs (* for any)")
	enableRemoteShell := flag.Bool("enable-remote-shell", false, "Enable remote shell execution on this node (requires allowlist)")
	shellAllowlist := flag.String("shell-allowlist", "", "Comma-separated peer IDs allowed to execute remote shell commands")
	allowUnsolicitedChunks := flag.Bool("allow-unsolicited-chunks", false, "Allow peers to push unsolicited chunks to local storage (capped at 64 MiB/peer)")
	headless := flag.Bool("headless", false, "Run in headless server mode without launching desktop window")
	port := flag.Int("port", 23080, "Port for GUI web server")
	listenHost := flag.String("listen", "127.0.0.1", "Host interface to bind the GUI server to (use 0.0.0.0 for LAN)")
	allowRemoteGUI := flag.Bool("allow-remote-gui", false, "Allow binding GUI server to non-loopback interfaces")
	devWebDir := flag.String("dev-web-dir", "", "Optional directory to serve live web assets from disk instead of embedded assets")

	flag.Parse()

	if *enableRemoteShell && strings.TrimSpace(*shellAllowlist) == "" {
		log.Fatalf("[FATAL] -enable-remote-shell is true but -shell-allowlist is empty; remote shell startup refused for security")
	}

	if *listenHost != "127.0.0.1" && *listenHost != "localhost" && *listenHost != "::1" && !*allowRemoteGUI {
		log.Fatalf("[FATAL] Refusing to bind GUI server to non-loopback interface '%s' without -allow-remote-gui flag\n", *listenHost)
	}

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

	var shellList []string
	if *shellAllowlist != "" {
		for _, s := range strings.Split(*shellAllowlist, ",") {
			if s = strings.TrimSpace(s); s != "" {
				shellList = append(shellList, s)
			}
		}
	}

	quotaBytes := *quotaGB * 1024 * 1024 * 1024
	nodeCfg := node.NodeConfig{
		NodeID:            *nodeID,
		PoolID:            *poolID,
		StorageDir:        *storageDir,
		StorageQuota:      quotaBytes,
		SignalingURL:      *signalingURL,
		AuthToken:         *authToken,
		STUNServersV4:                   stunListV4,
		STUNServersV6:                   stunListV6,
		EnableRemoteShell:               *enableRemoteShell,
		ShellAllowlist:                  shellList,
		AllowUnsolicitedChunks:          *allowUnsolicitedChunks,
		DisableUnsolicitedChunks:        !*allowUnsolicitedChunks,
		MaxUnsolicitedChunkBytesPerPeer: node.DefaultMaxUnsolicitedChunkBytesPerPeer,
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
			if *codeAllowlist != "" {
				for _, p := range strings.Split(*codeAllowlist, ",") {
					if p = strings.TrimSpace(p); p != "" {
						allowedPeers = append(allowedPeers, p)
					}
				}
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
	state.SetAllowRemoteGUI(*allowRemoteGUI)
	sessionToken := state.SessionToken()
	mux := http.NewServeMux()
	state.SetupRoutes(mux)

	// Serve web frontend assets from embedded FS by default; disk serving requires explicit -dev-web-dir
	var fileHandler http.Handler
	if dir := strings.TrimSpace(*devWebDir); dir != "" {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			log.Fatalf("[FATAL] -dev-web-dir %q is not a valid directory: %v\n", dir, err)
		}
		log.Printf("[GUI] Serving live web frontend assets from -dev-web-dir: %s\n", dir)
		fileHandler = http.FileServer(http.Dir(dir))
	} else {
		subFS, err := fs.Sub(embeddedWebFS, "web")
		if err != nil {
			log.Fatalf("[FATAL] Failed to access embedded web files: %v\n", err)
		}
		fileHandler = http.FileServer(http.FS(subFS))
	}
	mux.Handle("/", fileHandler)

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
	state.SetActivePort(actualPort)
	displayHost := *listenHost
	if displayHost == "" || displayHost == "0.0.0.0" {
		displayHost = "127.0.0.1"
	} else if displayHost == "::" {
		displayHost = "[::1]"
	} else if strings.Contains(displayHost, ":") && !strings.HasPrefix(displayHost, "[") {
		displayHost = "[" + displayHost + "]"
	}
	guiURL := fmt.Sprintf("http://%s:%d", displayHost, actualPort)
	desktopURL := fmt.Sprintf("%s/#token=%s", guiURL, sessionToken)

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
	fmt.Printf(" Local GUI URL:  %s\n", desktopURL)
	fmt.Println("=========================================================================")

	shutdownCh := make(chan struct{})
	var profileDir string

	// Launch dedicated native desktop app window unless headless
	if !*headless {
		var profErr error
		profileDir, profErr = createGUIProfileDir()
		if profErr != nil {
			log.Printf("[WARN] Could not create isolated browser profile dir: %v\n", profErr)
		} else {
			defer os.RemoveAll(profileDir)
		}
		go func() {
			time.Sleep(300 * time.Millisecond)
			log.Printf("[GUI] Launching native desktop window for %s...\n", desktopURL)
			cmd, err := launchDesktopWindow(desktopURL, profileDir)
			if err != nil {
				log.Printf("[WARN] Could not spawn desktop window (%v). Access GUI via: %s\n", err, desktopURL)
			} else if cmd != nil {
				_ = cmd.Wait() // When user closes desktop window, trigger graceful exit
				log.Println("[GUI] Desktop window closed by user. Exiting daemon...")
				close(shutdownCh)
			}
		}()
	} else {
		log.Printf("[HEADLESS] Running in headless mode. Open %s in your browser.\n", desktopURL)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sigCh:
	case <-shutdownCh:
	}

	log.Println("[INFO] Shutting down UniCompute GUI gracefully...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
}
