// Package main provides the doit command-line entrypoint.
package main

import (
	"os"

	"github.com/diakovliev/doit/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
