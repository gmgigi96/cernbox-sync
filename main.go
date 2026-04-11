package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/gmgigi96/cernbox-sync/engine"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmsgprefix)
	log.SetPrefix("")

	var (
		localDir  = flag.String("local", "", "Local directory to sync (required)")
		remoteURL = flag.String("remote", "", "Remote WebDAV URL of the folder to sync (required)")
		username  = flag.String("user", "", "WebDAV username (required)")
		password  = flag.String("pass", "", "WebDAV password (required)")
		dbPath    = flag.String("db", "", "Path to the SQLite state DB (default: <local>/.sync.db)")
	)
	flag.Parse()

	if *localDir == "" || *remoteURL == "" || *username == "" || *password == "" {
		fmt.Fprintf(os.Stderr, "Usage: sync -local <dir> -remote <url> -user <u> -pass <p> [-db <file>]\n\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	absLocal, err := filepath.Abs(*localDir)
	if err != nil {
		log.Fatalf("invalid local path: %v", err)
	}
	if err := os.MkdirAll(absLocal, 0o755); err != nil {
		log.Fatalf("cannot create local dir: %v", err)
	}

	stateDB := *dbPath
	if stateDB == "" {
		stateDB = filepath.Join(absLocal, ".sync.db")
	}

	cfg := engine.Config{
		LocalRoot:  absLocal,
		RemoteBase: *remoteURL,
		Username:   *username,
		Password:   *password,
		DBPath:     stateDB,
	}

	if err := engine.Run(cfg); err != nil {
		log.Fatalf("sync failed: %v", err)
	}
}
