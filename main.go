package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/gastownhall/tmux-adapter/internal/adapter"
)

func main() {
	gtDir := flag.String("gt-dir", filepath.Join(os.Getenv("HOME"), "gt"), "gastown town directory")
	port := flag.Int("port", 8080, "WebSocket server port")
	authToken := flag.String("auth-token", "", "optional WebSocket auth token (Bearer token or ?token=...)")
	allowedOrigins := flag.String("allowed-origins", "localhost:*", "comma-separated origin patterns for WebSocket CORS (e.g. \"localhost:*,myhost.example.com\")")
	flag.Parse()

	var origins []string
	for _, o := range strings.Split(*allowedOrigins, ",") {
		if s := strings.TrimSpace(o); s != "" {
			origins = append(origins, s)
		}
	}

	a := adapter.New(*gtDir, *port, *authToken, origins)
	if err := a.Start(); err != nil {
		log.Fatal(err)
	}

	// Wait for interrupt signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	a.Stop()
}
