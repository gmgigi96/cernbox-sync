package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"runtime"

	"github.com/gmgigi96/cernbox-sync/ipc"
	"github.com/gmgigi96/cernbox-sync/loginflow"
)

// defaultServerURL is the server the login flow runs against when -server is
// not given.
const defaultServerURL = "https://cernbox.cern.ch"

// ── login ────────────────────────────────────────────────────────────────────

// cmdLogin connects the account through the browser-based login flow: the
// user grants access in the browser and the server hands out an app password,
// which is stored in the daemon in place of the account password.
func cmdLogin(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	server := fs.String("server", defaultServerURL, "CERNBox server URL")
	noBrowser := fs.Bool("no-browser", false, "Do not open the browser automatically; only print the login URL")
	_ = fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	flow, err := loginflow.Start(ctx, *server)
	if err != nil {
		log.Fatalf("%v", err)
	}

	fmt.Println("To connect your account, open this URL in your browser and grant access:")
	fmt.Println()
	fmt.Println("  " + flow.LoginURL)
	fmt.Println()
	if !*noBrowser {
		if err := openBrowser(flow.LoginURL); err != nil {
			fmt.Fprintf(os.Stderr, "Could not open the browser (%v); open the URL manually.\n", err)
		}
	}
	fmt.Println("Waiting for access to be granted (Ctrl-C to cancel)…")

	ctx, cancel := context.WithTimeout(ctx, loginflow.DefaultTimeout)
	defer cancel()
	creds, err := flow.Wait(ctx, loginflow.DefaultPollInterval)
	switch {
	case err == nil:
	case ctx.Err() == context.DeadlineExceeded:
		log.Fatalf("login flow timed out: access was not granted within %s", loginflow.DefaultTimeout)
	case ctx.Err() != nil:
		log.Fatalf("login cancelled")
	default:
		log.Fatalf("%v", err)
	}

	send(ipc.Request{
		Cmd:     ipc.CmdSetAccount,
		Account: &ipc.AccountPayload{Username: creds.LoginName, Password: creds.AppPassword},
	})
	fmt.Printf("Logged in as %q\n", creds.LoginName)
}

// openBrowser opens url with the platform's default handler.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
