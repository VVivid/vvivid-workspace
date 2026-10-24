package main

import (
	"context"
	"fmt"
	"os"

	"github.com/VVivid/vvivid-workspace/WebhookControl/admincli"
)

func main() {
	if err := admincli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
