package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/unicompute/obj1/pkg/node"
	"github.com/unicompute/obj1/pkg/codeexec"
	"github.com/unicompute/obj1/pkg/relay"
	"github.com/unicompute/obj1/pkg/remoteshell"
	"github.com/unicompute/obj1/pkg/transport"
)

func main() {
	nodeID := flag.String("id", "", "Unique node identifier (default: generated UUID)")
	poolID := flag.String("pool", "default-pool", "Storage pool identifier")
	storageDir := flag.String("storage", "", "Storage directory for chunk blobs (default: ./unicompute_data)")
	quotaGB := flag.Int64("quota-gb", 10, "Contributed storage quota in GiB")
	signalingURL := flag.String("signaling", "ws://127.0.0.1:8080/ws", "Coordinator signaling WebSocket URL")
	stunServer := flag.String("stun", "stun.l.google.com:19302", "STUN server address (comma-separated for multiple)")
	stunServer6 := flag.String("stun6", "", "IPv6 STUN server address (comma-separated for multiple)")
	turnServer := flag.String("turn", "", "TURN server address (e.g., turn:relay.example.com:3478, comma-separated for multiple)")
	turnUser := flag.String("turn-user", "", "TURN server username")
	turnCred := flag.String("turn-cred", "", "TURN server credential / password")
	relayAddr := flag.String("relay", "", "Contributor relay address (optional)")
	runRelay := flag.Bool("run-relay", false, "Contribute relay bandwidth to the fleet (runs local contributor relay)")
	enableCodeExecution := flag.Bool("enable-code-execution", false, "Enable Docker sandbox code execution on this worker")
	codeAllowlist := flag.String("code-allowlist", "", "Comma-separated node IDs allowed to submit code jobs")
	codeTimeout := flag.Int("code-timeout", 10, "Code job timeout in seconds (1-30)")
	relayListen := flag.String("relay-listen", "0.0.0.0:22067", "Relay listen address if run-relay is enabled")

	flag.Parse()
	if *codeTimeout < 1 || *codeTimeout > codeexec.MaxTimeoutSeconds {
		log.Fatalf("-code-timeout must be between 1 and %d seconds", codeexec.MaxTimeoutSeconds)
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
			s = strings.TrimSpace(s)
			if s != "" {
				stunListV4 = append(stunListV4, s)
			}
		}
	}

	var stunListV6 []string
	if *stunServer6 != "" {
		for _, s := range strings.Split(*stunServer6, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				stunListV6 = append(stunListV6, s)
			}
		}
	}

	var turnConfigs []transport.TURNServerConfig
	if *turnServer != "" {
		for _, s := range strings.Split(*turnServer, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				turnConfigs = append(turnConfigs, transport.TURNServerConfig{
					URL:        s,
					Username:   *turnUser,
					Credential: *turnCred,
				})
			}
		}
	}

	fmt.Println("===================================================================")
	fmt.Println(" UniCompute Objective 1: Peer-to-Peer Storage Substrate")
	fmt.Printf(" Node ID:        %s\n", *nodeID)
	fmt.Printf(" Pool ID:        %s\n", *poolID)
	fmt.Printf(" Storage Quota:  %d GiB\n", *quotaGB)
	fmt.Printf(" Storage Path:   %s\n", *storageDir)
	fmt.Printf(" Signaling URL:  %s\n", *signalingURL)
	if len(stunListV4) > 0 {
		fmt.Printf(" STUN Server(s): %s\n", strings.Join(stunListV4, ", "))
	}
	if len(stunListV6) > 0 {
		fmt.Printf(" STUN IPv6:      %s\n", strings.Join(stunListV6, ", "))
	}
	if len(turnConfigs) > 0 {
		fmt.Printf(" TURN Server(s): %s\n", *turnServer)
	}
	fmt.Println("===================================================================")

	// If runRelay is enabled, start contributor relay
	var contributorRelay *relay.ContributorRelay
	if *runRelay {
		var err error
		contributorRelay, err = relay.NewContributorRelay(relay.ServerConfig{
			ListenAddr:     *relayListen,
			SessionTimeout: 30 * time.Second,
		})
		if err != nil {
			log.Printf("[WARN] Failed to start contributor relay: %v\n", err)
		} else {
			defer contributorRelay.Close()
			log.Printf("[INFO] Contributor relay running on %s\n", contributorRelay.Addr().String())
		}
	}

	quotaBytes := *quotaGB * 1024 * 1024 * 1024
	n, err := node.NewNode(node.NodeConfig{
		NodeID:          *nodeID,
		PoolID:          *poolID,
		StorageDir:      *storageDir,
		StorageQuota:    quotaBytes,
		SignalingURL:    *signalingURL,
		STUNServersV4:   stunListV4,
		STUNServersV6:   stunListV6,
		TURNServers:     turnConfigs,
		RelayAddr:       *relayAddr,
		FallbackTimeout: 2000 * time.Millisecond,
	})
	if err != nil {
		log.Fatalf("[FATAL] Failed to initialize node: %v\n", err)
	}
	if *enableCodeExecution {
		var allowedPeers []string
		for _, peerID := range strings.Split(*codeAllowlist, ",") {
			if peerID = strings.TrimSpace(peerID); peerID != "" {
				allowedPeers = append(allowedPeers, peerID)
			}
		}
		if len(allowedPeers) == 0 {
			log.Fatalf("-enable-code-execution requires -code-allowlist with one or more trusted peer IDs")
		}
		dockerCtx, cancelDocker := context.WithTimeout(context.Background(), 5*time.Second)
		dockerErr := codeexec.CheckDocker(dockerCtx)
		cancelDocker()
		if dockerErr != nil {
			log.Fatalf("code execution requested but Docker is unavailable: %v", dockerErr)
		}
		n.SetCodeExecutor(codeexec.NewDockerExecutor(codeexec.Config{
			DefaultTimeout: time.Duration(*codeTimeout) * time.Second,
			MaxTimeout:     time.Duration(codeexec.MaxTimeoutSeconds) * time.Second,
			MaxConcurrent:  2,
		}), allowedPeers...)
		log.Printf("[CODEEXEC] Docker sandbox enabled; allowed submitting peers: %s", strings.Join(allowedPeers, ", "))
	}
	defer n.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.Println("[INFO] Starting node and classifying network topology...")
	if err := n.Start(ctx); err != nil {
		log.Printf("[WARN] Node startup note: %v (running in offline/direct mode)\n", err)
	}

	// Print NAT & Network diagnostics
	natRep := n.NATReport()
	if natRep != nil {
		fmt.Println("\n--- Network Diagnostics ---")
		fmt.Printf(" NAT Classification: %s\n", natRep.NATType)
		fmt.Printf(" Behind CGNAT:        %v (RFC 6598 100.64.0.0/10)\n", natRep.IsBehindCGNAT)
		fmt.Printf(" IPv4 Available:      %v (Public: %s)\n", natRep.IPv4Supported, natRep.PublicIPv4)
		fmt.Printf(" IPv6 Available:      %v (Public: %s)\n", natRep.IPv6Supported, natRep.PublicIPv6)
		fmt.Printf(" Hole-Punch Feasible: %v\n", natRep.HolePunchFeasible)
		fmt.Printf(" Requires Relay:      %v\n", natRep.RequiresRelay)
		if len(natRep.FilteredInterfaces) > 0 {
			fmt.Printf(" Pruned Virtual Adapters: %v\n", natRep.FilteredInterfaces)
		}
		fmt.Println("---------------------------")
	}

	// Event hooks
	n.SetOnChunkReceived(func(peerID, chunkID string, size int64) {
		fmt.Printf("\n[EVENT] Chunk %s (%d bytes) received from peer %s and stored\n> ", chunkID, size, peerID)
	})

	n.SetOnStreamMessage(func(peerID string, data []byte) {
		msgStr := string(data)
		if strings.HasPrefix(msgStr, "__PING__:") {
			ts := strings.TrimPrefix(msgStr, "__PING__:")
			_ = n.SendStream(peerID, []byte("__PONG__:"+ts))
			return
		}
		if strings.HasPrefix(msgStr, "__PONG__:") {
			tsStr := strings.TrimPrefix(msgStr, "__PONG__:")
			if tNano, err := time.ParseDuration(fmt.Sprintf("%sns", tsStr)); err == nil {
				_ = tNano
			}
			fmt.Printf("\n[PONG] Reply from %s: Pong received!\n> ", peerID)
			return
		}
		fmt.Printf("\n[STREAM] Message from %s: %s\n> ", peerID, msgStr)
	})

	n.SetOnPeerConnected(func(peerID string) {
		fmt.Printf("\n[EVENT] Peer %s connected via WebRTC DataChannel\n", peerID)
		if m, ok := n.GetPathMetrics(peerID); ok {
			fmt.Printf("[METRICS] Peer %s path: direct=%v, pair=%s, local=%s, remote=%s\n> ",
				peerID, m.IsDirect, m.PairType, m.LocalCandidate, m.RemoteCandidate)
		} else {
			fmt.Print("> ")
		}
	})

	n.SetOnFleetPeerJoined(func(peerID string) {
		fmt.Printf("\n[FLEET] Peer '%s' joined the fleet! (Type 'connect %s' to establish P2P link)\n> ", peerID, peerID)
	})

	n.SetOnFleetPeerLeft(func(peerID string) {
		fmt.Printf("\n[FLEET] Peer '%s' disconnected from the fleet.\n> ", peerID)
	})

	// Initial fleet discovery
	fleetNodes, _ := n.GetFleetNodes(context.Background())
	if len(fleetNodes) > 0 {
		fmt.Printf("[FLEET] %d other peer(s) currently online: %v\n", len(fleetNodes), fleetNodes)
	} else {
		fmt.Println("[FLEET] Connected to coordinator. Waiting for other laptops to connect...")
	}

	// Periodic telemetry reporting
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-ticker.C:
				usage := n.StorageUsage()
				log.Printf("[STATUS] Storage: %d / %d MB used (%.1f%%) | Blobs: %d\n",
					usage.UsedBytes/(1024*1024), usage.QuotaBytes/(1024*1024),
					(float64(usage.UsedBytes)/float64(usage.QuotaBytes))*100, usage.BlobCount)
			case <-ctx.Done():
				return
			}
		}
	}()

	// Interactive Console Loop
	fmt.Println("\nInteractive Mode Active!")
	fmt.Println("Type 'help' to see commands, or Ctrl+C to exit.")
	fmt.Print("> ")

	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				fmt.Print("> ")
				continue
			}
			parts := splitConsoleArgs(line)
			cmd := strings.ToLower(parts[0])

			switch cmd {
			case "help":
				fmt.Println("Available Commands:")
				fmt.Println("  peers, users, nodes            - List all online fleet nodes & active P2P links")
				fmt.Println("  connect <peerID>               - Establish P2P WebRTC link with a peer")
				fmt.Println("  disconnect <peerID>            - Disconnect active P2P link with a peer")
				fmt.Println("  ping <peerID>                  - Ping a connected peer over SCTP stream channel")
				fmt.Println("  stream <peerID> <message>      - Send live stream message/log to a peer")
				fmt.Println("  put <filePath>                 - Store a local file into the disk vault")
				fmt.Println("  get <blobID> [destFile]        - Retrieve and view/export a stored blob")
				fmt.Println("  transfer <peerID> <blobID>     - Transfer a blob to peer over WebRTC")
				fmt.Println("  fetch <peerID> <blobID> [sz]   - Fetch a blob from peer over WebRTC")
				fmt.Println("  list, ls                       - List all stored blobs in the local vault")
				fmt.Println("  stat <blobID>                  - Show metadata for a stored blob")
				fmt.Println("  del <blobID>                   - Delete a stored blob from vault")
				fmt.Println("  scrub                          - Scrub blob store for bitrot")
				fmt.Println("  status, info                   - Display full local node and network status")
				fmt.Println("  metrics                        - Display live path metrics")
				fmt.Println("  nat                            - Show local NAT diagnostics")
				fmt.Println("  exec <peerID|all> <command...> - Execute shell command on connected remote PC(s)")
				fmt.Println("  shell <peerID|all> <cmd...>    - Alias for remote command execution")
				fmt.Println("  run <peerID> <c|cpp|python> <source-file> [stdin-file] - Run source code in a Docker sandbox")
				fmt.Println("  quit, exit                     - Terminate the node")
			case "peers", "users", "nodes":
				fleetList, err := n.GetFleetNodes(context.Background())
				connectedPeers := n.GetConnectedPeers()

				connectedMap := make(map[string]bool)
				for _, p := range connectedPeers {
					connectedMap[p] = true
				}

				fmt.Println("\n=================== UniCompute Network Status ===================")
				fmt.Printf(" Local Node ID:  %s\n", *nodeID)
				fmt.Printf(" Signaling URL:  %s\n", *signalingURL)
				fmt.Println("-----------------------------------------------------------------")
				
				fmt.Printf(" Available Fleet Nodes (%d online):\n", len(fleetList))
				if len(fleetList) == 0 {
					fmt.Println("   (No other nodes currently registered with coordinator)")
				} else {
					for _, fn := range fleetList {
						if connectedMap[fn] {
							fmt.Printf("   [*] %s (Connected P2P)\n", fn)
						} else {
							fmt.Printf("   [*] %s (Online on coordinator - type 'connect %s')\n", fn, fn)
						}
					}
				}

				fmt.Println("-----------------------------------------------------------------")
				fmt.Printf(" Active P2P DataChannels (%d connected):\n", len(connectedPeers))
				if len(connectedPeers) == 0 {
					fmt.Println("   (No active P2P links. Connect using 'connect <peerID>')")
				} else {
					for i, p := range connectedPeers {
						pathStr := "P2P DataChannel"
						if m, ok := n.GetPathMetrics(p); ok {
							pathStr = fmt.Sprintf("direct=%v | pair=%s | local=%s | remote=%s",
								m.IsDirect, m.PairType, m.LocalCandidate, m.RemoteCandidate)
						}
						fmt.Printf("   [%d] %s [%s]\n", i+1, p, pathStr)
					}
				}
				if err != nil {
					fmt.Printf(" [NOTE] Coordinator query note: %v\n", err)
				}
				fmt.Println("=================================================================")
			case "disconnect":
				if len(parts) < 2 {
					fmt.Println("Usage: disconnect <peerID>")
					break
				}
				peerID := parts[1]
				if err := n.DisconnectPeer(peerID); err != nil {
					fmt.Printf("Disconnect failed: %v\n", err)
				} else {
					fmt.Printf("Disconnected P2P link with %s.\n", peerID)
				}
			case "ping":
				if len(parts) < 2 {
					fmt.Println("Usage: ping <peerID>")
					break
				}
				peerID := parts[1]
				nowNano := fmt.Sprintf("%d", time.Now().UnixNano())
				if err := n.SendStream(peerID, []byte("__PING__:"+nowNano)); err != nil {
					fmt.Printf("Ping failed: %v (are you connected? type 'connect %s')\n", err, peerID)
				} else {
					fmt.Printf("Ping sent to %s...\n", peerID)
				}
			case "status", "info":
				usage := n.StorageUsage()
				connectedPeers := n.GetConnectedPeers()
				rep := n.NATReport()

				fmt.Println("\n==================== Local Node Diagnostics ====================")
				fmt.Printf(" Node ID:        %s\n", *nodeID)
				fmt.Printf(" Storage Path:   %s\n", *storageDir)
				fmt.Printf(" Storage Quota:  %d MB total | %d MB used (%.1f%%) | %d Blobs\n",
					usage.QuotaBytes/(1024*1024), usage.UsedBytes/(1024*1024),
					(float64(usage.UsedBytes)/float64(usage.QuotaBytes))*100, usage.BlobCount)
				fmt.Printf(" Signaling Hub:  %s\n", *signalingURL)
				fmt.Printf(" Connected P2P:  %d peer(s) %v\n", len(connectedPeers), connectedPeers)
				if rep != nil {
					fmt.Printf(" NAT Type:       %s (CGNAT: %v, HolePunch: %v)\n",
						rep.NATType, rep.IsBehindCGNAT, rep.HolePunchFeasible)
				}
				fmt.Println("================================================================")
			case "get":
				if len(parts) < 2 {
					fmt.Println("Usage: get <blobID> [destinationPath]")
					break
				}
				blobID := parts[1]
				rc, err := n.GetBlob(blobID)
				if err != nil {
					fmt.Printf("Error getting blob: %v\n", err)
					break
				}
				defer rc.Close()

				if len(parts) >= 3 {
					destFile := parts[2]
					outFile, err := os.Create(destFile)
					if err != nil {
						fmt.Printf("Error creating output file: %v\n", err)
						break
					}
					written, err := io.Copy(outFile, rc)
					outFile.Close()
					if err != nil {
						fmt.Printf("Error saving blob: %v\n", err)
					} else {
						fmt.Printf("Exported blob %s (%d bytes) to %s\n", blobID, written, destFile)
					}
				} else {
					data, err := io.ReadAll(io.LimitReader(rc, 256))
					if err != nil {
						fmt.Printf("Read error: %v\n", err)
					} else {
						fmt.Printf("Blob %s Preview: %s\n", blobID, string(data))
					}
				}
			case "fetch":
				if len(parts) < 3 {
					fmt.Println("Usage: fetch <peerID> <blobID> [expectedSize]")
					break
				}
				peerID, blobID := parts[1], parts[2]
				var expectedSize int64
				if len(parts) >= 4 {
					fmt.Sscanf(parts[3], "%d", &expectedSize)
				}
				start := time.Now()
				fetchCtx, cancelFetch := context.WithTimeout(context.Background(), 30*time.Second)
				err := n.FetchChunk(fetchCtx, peerID, blobID, expectedSize)
				cancelFetch()
				if err != nil {
					fmt.Printf("Fetch failed: %v\n", err)
				} else {
					fmt.Printf("Successfully fetched blob %s from %s in %v\n", blobID, peerID, time.Since(start))
				}
			case "put":
				if len(parts) < 2 {
					fmt.Println("Usage: put <filePath>")
					break
				}
				filePath := parts[1]
				f, err := os.Open(filePath)
				if err != nil {
					fmt.Printf("Error opening file: %v\n", err)
					break
				}
				hasher := sha256.New()
				if _, err := io.Copy(hasher, f); err != nil {
					f.Close()
					fmt.Printf("Error hashing file: %v\n", err)
					break
				}
				blobID := hex.EncodeToString(hasher.Sum(nil))
				if _, err := f.Seek(0, io.SeekStart); err != nil {
					f.Close()
					fmt.Printf("Error rewinding file: %v\n", err)
					break
				}
				written, err := n.PutBlob(blobID, f)
				f.Close()
				if err != nil {
					fmt.Printf("PutBlob failed: %v\n", err)
				} else {
					fmt.Printf("Successfully stored blob: %s (%d bytes)\n", blobID, written)
				}
			case "list", "ls":
				blobs, err := n.ListBlobs()
				if err != nil {
					fmt.Printf("Error listing blobs: %v\n", err)
					break
				}
				fmt.Printf("Stored Blobs in Local Vault (%d total):\n", len(blobs))
				if len(blobs) == 0 {
					fmt.Println("  (Storage vault is currently empty. Use 'put <file>' to store blobs)")
				} else {
					for i, b := range blobs {
						sizeKB := float64(b.Size) / 1024.0
						fmt.Printf("  [%d] ID: %s | Size: %d bytes (%.2f KB) | Mod: %s\n",
							i+1, b.ID, b.Size, sizeKB, b.ModTime.Format("2006-01-02 15:04:05"))
					}
				}
			case "stat":
				if len(parts) < 2 {
					fmt.Println("Usage: stat <blobID>")
					break
				}
				info, err := n.StatBlob(parts[1])
				if err != nil {
					fmt.Printf("Stat error: %v\n", err)
				} else {
					fmt.Printf("Blob: %s | Size: %d bytes | Mod: %s | SHA256: %s\n", info.ID, info.Size, info.ModTime.Format(time.RFC3339), info.SHA256)
				}
			case "del", "rm":
				if len(parts) < 2 {
					fmt.Println("Usage: del <blobID>")
					break
				}
				if err := n.DeleteBlob(parts[1]); err != nil {
					fmt.Printf("Delete error: %v\n", err)
				} else {
					fmt.Printf("Blob %s deleted successfully\n", parts[1])
				}
			case "scrub":
				corrupted, err := n.Scrub()
				if err != nil {
					fmt.Printf("Scrub error: %v\n", err)
				} else if len(corrupted) == 0 {
					fmt.Println("Scrub complete: all stored blobs passed cryptographic SHA-256 verification.")
				} else {
					fmt.Printf("WARNING: Found %d corrupt blobs: %v\n", len(corrupted), corrupted)
				}
			case "connect":
				if len(parts) < 2 {
					fmt.Println("Usage: connect <peerID>")
					break
				}
				peerID := parts[1]
				fmt.Printf("Establishing WebRTC P2P link with %s...\n", peerID)
				connCtx, cancelConn := context.WithTimeout(context.Background(), 15*time.Second)
				_, err := n.ConnectPeer(connCtx, peerID)
				cancelConn()
				if err != nil {
					fmt.Printf("Connection to %s failed: %v\n", peerID, err)
				} else {
					fmt.Printf("WebRTC DataChannel connected to %s!\n", peerID)
				}
			case "transfer":
				if len(parts) < 3 {
					fmt.Println("Usage: transfer <peerID> <blobID>")
					break
				}
				peerID, blobID := parts[1], parts[2]
				start := time.Now()
				txCtx, cancelTx := context.WithTimeout(context.Background(), 30*time.Second)
				err := n.TransferChunk(txCtx, peerID, blobID)
				cancelTx()
				if err != nil {
					fmt.Printf("Transfer failed: %v\n", err)
				} else {
					fmt.Printf("Transferred blob %s to %s in %v\n", blobID, peerID, time.Since(start))
				}
			case "stream":
				if len(parts) < 3 {
					fmt.Println("Usage: stream <peerID> <message...>")
					break
				}
				peerID := parts[1]
				msg := strings.Join(parts[2:], " ")
				if err := n.SendStream(peerID, []byte(msg)); err != nil {
					fmt.Printf("Stream error: %v\n", err)
				} else {
					fmt.Printf("Stream message sent to %s\n", peerID)
				}
			case "metrics":
				stats := n.FleetStats()
				fmt.Printf("Fleet Metrics: Total=%d | Direct=%d | Relayed=%d | Direct%%=%.1f%% | CGNAT%%=%.1f%%\n",
					stats.TotalConnections, stats.DirectConnections, stats.RelayedConnections, stats.DirectConnectionPct, stats.CGNATPrevalencePct)
			case "nat":
				rep := n.NATReport()
				if rep != nil {
					fmt.Printf("NAT: %s | CGNAT: %v | HolePunchFeasible: %v | IPv4: %s | IPv6: %s\n",
						rep.NATType, rep.IsBehindCGNAT, rep.HolePunchFeasible, rep.PublicIPv4, rep.PublicIPv6)
				}
			case "exec", "shell":
				if len(parts) < 3 {
					fmt.Println("Usage: exec <peerID|all> <command...>")
					fmt.Println("Example: exec node-a1b2 whoami")
					fmt.Println("Example: exec all Get-Process")
					break
				}
				targetPeer := parts[1]
				commandStr := strings.Join(parts[2:], " ")

				if targetPeer == "all" {
					activePeers := n.GetConnectedPeers()
					if len(activePeers) == 0 {
						fmt.Println("[EXEC] No connected remote peers. Establish connection first via 'connect <peerID>'.")
						break
					}
					fmt.Printf("[EXEC] Broadcasting '%s' to %d connected remote peer(s)...\n", commandStr, len(activePeers))
					execCtx, cancelExec := context.WithTimeout(context.Background(), 45*time.Second)
					results := n.ExecuteRemoteCommandAll(execCtx, remoteshell.ShellRequest{
						Command:   commandStr,
						ShellType: remoteshell.ShellAuto,
					})
					cancelExec()

					for pid, res := range results {
						fmt.Println("=================================================================")
						fmt.Printf(" Remote Node: %s | Exit Code: %d | Duration: %d ms\n", pid, res.ExitCode, res.DurationMs)
						if res.Error != "" {
							fmt.Printf(" [!] Error: %s\n", res.Error)
						}
						if res.Stdout != "" {
							fmt.Println(" --- STDOUT ---")
							fmt.Print(res.Stdout)
							if !strings.HasSuffix(res.Stdout, "\n") {
								fmt.Println()
							}
						}
						if res.Stderr != "" {
							fmt.Println(" --- STDERR ---")
							fmt.Print(res.Stderr)
							if !strings.HasSuffix(res.Stderr, "\n") {
								fmt.Println()
							}
						}
						fmt.Println("=================================================================")
					}
				} else {
					fmt.Printf("[EXEC] Executing '%s' on remote PC '%s'...\n", commandStr, targetPeer)
					execCtx, cancelExec := context.WithTimeout(context.Background(), 45*time.Second)
					res, err := n.ExecuteRemoteCommand(execCtx, targetPeer, remoteshell.ShellRequest{
						Command:   commandStr,
						ShellType: remoteshell.ShellAuto,
					})
					cancelExec()

					if err != nil {
						fmt.Printf("[EXEC] Remote execution failed: %v\n", err)
						break
					}

					fmt.Println("=================================================================")
					fmt.Printf(" Remote Node: %s | Exit Code: %d | Duration: %d ms\n", targetPeer, res.ExitCode, res.DurationMs)
					if res.Error != "" {
						fmt.Printf(" [!] Error: %s\n", res.Error)
					}
					if res.Stdout != "" {
						fmt.Println(" --- STDOUT ---")
						fmt.Print(res.Stdout)
						if !strings.HasSuffix(res.Stdout, "\n") {
							fmt.Println()
						}
					}
					if res.Stderr != "" {
						fmt.Println(" --- STDERR ---")
						fmt.Print(res.Stderr)
						if !strings.HasSuffix(res.Stderr, "\n") {
							fmt.Println()
						}
					}
					fmt.Println("=================================================================")
				}
			case "run":
				if len(parts) < 4 || len(parts) > 5 {
					fmt.Println("Usage: run <peerID> <c|cpp|python> <source-file> [stdin-file]")
					fmt.Println(`Example: run node-b python "./hello world.py"`)
					break
				}
				peerID := parts[1]
				language := strings.ToLower(parts[2])
				if language != "c" && language != "cpp" && language != "python" {
					fmt.Println("Supported languages: c, cpp, python")
					break
				}
				fileInfo, statErr := os.Stat(parts[3])
				if statErr != nil {
					fmt.Printf("Cannot access source file: %v\n", statErr)
					break
				}
				if !fileInfo.Mode().IsRegular() || fileInfo.Size() > int64(codeexec.MaxSourceBytes) {
					fmt.Printf("Source file must be a regular file no larger than %d bytes.\n", codeexec.MaxSourceBytes)
					break
				}
				sourceBytes, readErr := os.ReadFile(parts[3])
				if readErr != nil {
					fmt.Printf("Cannot read source file: %v\n", readErr)
					break
				}
				stdin := ""
				if len(parts) == 5 {
					inputInfo, inputStatErr := os.Stat(parts[4])
					if inputStatErr != nil {
						fmt.Printf("Cannot access stdin file: %v\n", inputStatErr)
						break
					}
					if !inputInfo.Mode().IsRegular() || inputInfo.Size() > int64(codeexec.MaxStdinBytes) {
						fmt.Printf("Stdin file must be a regular file no larger than %d bytes.\n", codeexec.MaxStdinBytes)
						break
					}
					inputBytes, inputReadErr := os.ReadFile(parts[4])
					if inputReadErr != nil {
						fmt.Printf("Cannot read stdin file: %v\n", inputReadErr)
						break
					}
					stdin = string(inputBytes)
				}
				req := codeexec.Request{
					JobID: "job-" + uuid.New().String(), Language: language,
					Source: string(sourceBytes), Stdin: stdin, TimeoutSeconds: *codeTimeout,
				}
				fmt.Printf("[RUN] Submitting %s job %s to %s...\n", language, req.JobID, peerID)
				jobStart := time.Now()
				execCtx, cancelExec := context.WithTimeout(context.Background(), time.Duration(*codeTimeout+8)*time.Second)
				resp, execErr := n.ExecuteRemoteCode(execCtx, peerID, req)
				cancelExec()
				if execErr != nil {
					fmt.Printf("[RUN] Job failed: %v\n", execErr)
					break
				}
				fmt.Println("=================================================================")
				fmt.Printf(" Job ID: %s | Worker: %s | Language: %s\n", resp.JobID, peerID, resp.Language)
				if resp.TimedOut {
					fmt.Println(" Status: Timed out")
				} else if resp.ExitCode == 0 && resp.Error == "" {
					fmt.Println(" Status: Completed")
				} else {
					fmt.Println(" Status: Failed")
				}
				if resp.Error != "" { fmt.Printf(" Error: %s\n", resp.Error) }
				if resp.Stdout != "" {
					fmt.Println(" --- STDOUT ---")
					fmt.Print(resp.Stdout)
					if !strings.HasSuffix(resp.Stdout, "\n") { fmt.Println() }
				}
				if resp.Stderr != "" {
					fmt.Println(" --- STDERR ---")
					fmt.Print(resp.Stderr)
					if !strings.HasSuffix(resp.Stderr, "\n") { fmt.Println() }
				}
				if resp.OutputTruncated { fmt.Println(" [!] Output was truncated by the worker's output limit.") }
				fmt.Printf(" Exit code: %d | Worker execution: %d ms | Round trip: %d ms\n", resp.ExitCode, resp.DurationMs, time.Since(jobStart).Milliseconds())
				fmt.Println("=================================================================")
			case "quit", "exit":
				fmt.Println("Exiting node...")
				os.Exit(0)
			default:
				fmt.Printf("Unknown command: %s. Type 'help' for command list.\n", cmd)
			}
			fmt.Print("> ")
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	log.Println("[INFO] Shutting down UniCompute node gracefully...")
}

