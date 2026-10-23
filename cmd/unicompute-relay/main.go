package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/unicompute/obj1/pkg/relay"
)

func main() {
	listenAddr := flag.String("listen", "0.0.0.0:22067", "Contributor relay listen address")
	timeoutSec := flag.Int("timeout", 30, "Session timeout in seconds")

	flag.Parse()

	fmt.Println("===================================================================")
	fmt.Println(" UniCompute Contributor Relay (Syncthing Relay-v1 Model)")
	fmt.Printf(" Listening on: %s\n", *listenAddr)
	fmt.Println(" Opaque byte forwarding with blind end-to-end encryption")
	fmt.Println("===================================================================")

	r, err := relay.NewContributorRelay(relay.ServerConfig{
		ListenAddr:     *listenAddr,
		SessionTimeout: time.Duration(*timeoutSec) * time.Second,
	})
	if err != nil {
		log.Fatalf("[FATAL] Failed to start contributor relay: %v\n", err)
	}
	defer r.Close()

	log.Printf("[INFO] Contributor relay active on %s\n", r.Addr().String())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	log.Printf("[INFO] Total bytes relayed: %d\n", r.TotalRelayedBytes())
	log.Println("[INFO] Shutting down contributor relay...")
}
