package main

import (
	"context"
	"fmt"
	"os"

	"ray-train-platform-backend/rayctl"
)

func main() {
	if err := rayctl.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "rayctl:", err)
		os.Exit(1)
	}
}
