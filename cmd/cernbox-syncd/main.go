// cernbox-syncd is the background daemon for cernbox-sync.
//
// It periodically syncs all registered folder pairs and serves IPC requests
// from the cernbox-sync CLI on a Unix domain socket.
//
// Usage:
//
//	cernbox-syncd [-interval 5m] [-socket /path/to/sync.sock] [-log-level info]
package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gmgigi96/cernbox-sync/config"
	"github.com/gmgigi96/cernbox-sync/daemon"
	"github.com/gmgigi96/cernbox-sync/ipc"
	"github.com/gmgigi96/cernbox-sync/logger"
)

func main() {
	interval := flag.Duration("interval", 5*time.Minute, "How often to sync all registered folders automatically")
	sockPath := flag.String("socket", "", "Unix socket path (default: platform-specific, see ipc.SocketPath)")
	logLevel := flag.String("log-level", "info", "Log verbosity: off, error, info, debug, trace")
	flag.Parse()

	level, err := logger.ParseLevel(*logLevel)
	if err != nil {
		log.Fatalf("log-level: %v", err)
	}

	l := logger.New(os.Stderr, level)
	logger.SetDefault(l)

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
	defer func() { _ = cfgDB.Close() }()

	d := daemon.New(cfgDB, *interval, l)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Graceful shutdown on SIGINT / SIGTERM.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		s := <-sig
		slog.Info("[daemon] received signal, shutting down…", "signal", s)
		cancel()
	}()

	if err := d.Run(ctx, *sockPath); err != nil {
		log.Fatalf("daemon: %v", err)
	}
}
