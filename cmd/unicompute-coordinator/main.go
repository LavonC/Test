package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/unicompute/obj1/pkg/relay"
	"github.com/unicompute/obj1/pkg/signaling"
)

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func main() {
	defaultListen := "0.0.0.0:8080"
	if p := os.Getenv("PORT"); p != "" {
		defaultListen = "0.0.0.0:" + p
	}
	defaultListen = getEnv("UNICOMPUTE_LISTEN", defaultListen)
	defaultPublicIP := getEnv("UNICOMPUTE_PUBLIC_IP", "127.0.0.1")
	defaultRealm := getEnv("UNICOMPUTE_REALM", "unicompute.edu")
	turnSecret := getEnv("UNICOMPUTE_TURN_SECRET", "")
	defaultEnableTURN := true
	if os.Getenv("UNICOMPUTE_TURN") == "false" || os.Getenv("ENABLE_TURN") == "false" {
		defaultEnableTURN = false
	}

	listenAddr := flag.String("listen", defaultListen, "Signaling server listen address")
	enableTURN := flag.Bool("turn", defaultEnableTURN, "Enable embedded TURN server")
	turnPort := flag.Int("turn-port", 3478, "Embedded TURN UDP port")
	turnRealm := flag.String("realm", defaultRealm, "TURN authentication realm")
	publicIP := flag.String("public-ip", defaultPublicIP, "Public IP for TURN relay")
	authToken := flag.String("auth-token", os.Getenv("UNICOMPUTE_AUTH_TOKEN"), "Cluster auth secret token")
	insecureNoAuth := flag.Bool("insecure-no-auth", false, "Disable authentication for local dev")

	flag.Parse()

	isLoopback := strings.HasPrefix(*listenAddr, "127.0.0.1:") || strings.HasPrefix(*listenAddr, "localhost:") || strings.HasPrefix(*listenAddr, "[::1]:")
	if *authToken == "" && !*insecureNoAuth && !isLoopback {
		tokenBytes := make([]byte, 32)
		if _, err := rand.Read(tokenBytes); err == nil {
			*authToken = hex.EncodeToString(tokenBytes)
			log.Printf("[SECURITY] Generated random cluster auth token: %s\n", *authToken)
		}
	}

	fmt.Println("===================================================================")
	fmt.Println(" UniCompute Signaling Hub & Coordinator Control Plane")
	fmt.Printf(" Listening on:  %s\n", *listenAddr)
	fmt.Printf(" Public IP:     %s\n", *publicIP)
	fmt.Printf(" Embedded TURN: %v (UDP :%d, Realm: %s)\n", *enableTURN, *turnPort, *turnRealm)
	if *authToken != "" {
		fmt.Printf(" Auth Token:    [CONFIGURED]\n")
	} else {
		fmt.Printf(" Auth Token:    [DISABLED / INSECURE]\n")
	}
	fmt.Println(" Invariant CP-1: Control Plane is decoupled from Data Plane")
	fmt.Println("===================================================================")

	hub, err := signaling.NewHub(*listenAddr, *authToken)
	if err != nil {
		log.Fatalf("[FATAL] Failed to start signaling hub: %v\n", err)
	}
	defer hub.Close()

	log.Printf("[INFO] Signaling WebSocket ready at %s\n", hub.URL())
	log.Printf("[INFO] Health check available at http://%s/health\n", *listenAddr)
	log.Printf("[INFO] Online nodes API at http://%s/api/nodes\n", *listenAddr)

	if *enableTURN {
		turnUser := os.Getenv("UNICOMPUTE_TURN_USER")
		turnPass := os.Getenv("UNICOMPUTE_TURN_PASS")
		users := make(map[string]string)
		if turnUser != "" && turnPass != "" {
			users[turnUser] = turnPass
		}

		if turnSecret == "" && len(users) == 0 {
			log.Println("[WARN] Embedded TURN enabled without credentials (set UNICOMPUTE_TURN_USER/UNICOMPUTE_TURN_PASS or UNICOMPUTE_TURN_SECRET). Skipping TURN startup.")
		} else {
			turnSrv, err := relay.NewEmbeddedTURN(relay.TURNConfig{
				Port:     *turnPort,
				Realm:    *turnRealm,
				PublicIP: *publicIP,
				Secret:   turnSecret,
				Users:    users,
			})
			if err != nil {
				log.Printf("[WARN] Failed to start embedded TURN: %v\n", err)
			} else {
				defer turnSrv.Close()
				log.Printf("[INFO] Embedded TURN server listening on UDP %s\n", turnSrv.Addr())
			}
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	log.Println("[INFO] Shutting down signaling hub...")
}
