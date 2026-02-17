package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/gastownhall/tmux-adapter/internal/converter"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: tmux-converter [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Streams structured conversation events from CLI AI agents over WebSocket.\n")
		fmt.Fprintf(os.Stderr, "Watches conversation files written by Claude Code, Codex, and Gemini,\n")
		fmt.Fprintf(os.Stderr, "parses them into normalized JSON events, and streams to connected clients.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  tmux-converter\n")
		fmt.Fprintf(os.Stderr, "  tmux-converter --work-dir ~/projects\n")
		fmt.Fprintf(os.Stderr, "  tmux-converter --listen :9090\n")
		fmt.Fprintf(os.Stderr, "  tmux-converter --debug-serve-dir ./samples\n")
	}

	var workDir string
	flag.StringVar(&workDir, "work-dir", "", "optional working directory filter — only track agents under this path (empty = all)")
	listen := flag.String("listen", ":8081", "HTTP/WebSocket listen address")
	debugServeDir := flag.String("debug-serve-dir", "", "serve static files from this directory at / (development only)")
	flag.Parse()

	c := converter.New(workDir, *listen, *debugServeDir)
	if err := c.Start(); err != nil {
		log.Fatal(err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	c.Stop()
}
