// cernbox-syncd is the background daemon for cernbox-sync.
//
// It periodically syncs all registered folder pairs and serves IPC requests
// from the cernbox-sync CLI on a Unix domain socket.
//
// Usage:
//
//	cernbox-syncd [-interval 5m] [-socket /path/to/sync.sock]
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gmgigi96/cernbox-sync/config"
	"github.com/gmgigi96/cernbox-sync/daemon"
	"github.com/gmgigi96/cernbox-sync/ipc"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmsgprefix)
	log.SetPrefix("")

	interval := flag.Duration("interval", 5*time.Minute, "How often to sync all registered folders automatically")
	sockPath := flag.String("socket", "", "Unix socket path (default: platform-specific, see ipc.SocketPath)")
	flag.Parse()

	if *sockPath == "" {
		var err error
		*sockPath, err = ipc.SocketPath()
		if err != nil {
			log.Fatalf("socket path: %v", err)
		}
	}

	cfgPath, err := config.DefaultPath()
	if err != nil {
		log.Fatalf("config path: %v", err)
	}
	cfgDB, err := config.Open(cfgPath)
	if err != nil {
		log.Fatalf("open config db: %v", err)
	}
	defer cfgDB.Close()

	d := daemon.New(cfgDB, *interval)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Graceful shutdown on SIGINT / SIGTERM.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		s := <-sig
		log.Printf("[daemon] received %s, shutting down…", s)
		cancel()
	}()

	if err := d.Run(ctx, *sockPath); err != nil {
		log.Fatalf("daemon: %v", err)
	}
}
