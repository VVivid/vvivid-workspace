package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/VVivid/vvivid-workspace/WebhookControl/daemon"
)

func main() {
	configPath := flag.String("config", "", "trusted daemon JSON configuration file")
	version := flag.Bool("version", false, "print executable version")
	checkReady := flag.Bool("check-ready", false, "probe configured listener readiness without starting service")
	flag.Parse()
	if *version {
		fmt.Println("webhook-control 0.1.0")
		return
	}
	if *configPath == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "-config is required; no positional arguments are accepted")
		os.Exit(2)
	}
	config, err := daemon.LoadConfig(*configPath)
	if err == nil {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if *checkReady {
			err = daemon.CheckReady(ctx, config)
		} else {
			err = daemon.Run(ctx, config)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
