package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/rokoucha/constulto/internal/cli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer cancel()
	os.Exit(cli.New().Run(ctx, os.Args[1:]))
}
