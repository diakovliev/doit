// Package main provides the doit command-line entrypoint.
package main

import (
	"io"
	"os"

	"github.com/diakovliev/doit/internal/app"
	"github.com/diakovliev/doit/internal/cli"
)

func main() {
	// os.Args[1:] excludes the program name.
	// Ensure we don't accidentally propagate a non-zero exit code from a
	// nil/invalid return value.
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return cli.RunWithHandler(args, stdin, stdout, stderr, app.Handler{})
}
