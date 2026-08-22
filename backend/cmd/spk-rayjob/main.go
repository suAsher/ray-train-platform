package main

import (
	"context"
	"fmt"
	"os"

	"ray-train-platform-backend/spkrayjob"
)

func main() {
	if err := spkrayjob.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "spk-rayjob:", err)
		os.Exit(1)
	}
}
