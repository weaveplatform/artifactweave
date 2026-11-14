package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/weaveplatform/artifactweave/internal/devtools/optable"
)

func main() {
	api := flag.String("api", "api", "directory holding the bundled <service>.openapi.yaml specs")
	out := flag.String(
		"out",
		"internal/platform/gen/operations/operations.gen.go",
		"the Go file to write; its directory names the package",
	)
	flag.Parse()
	n, err := optable.Run(*api, *out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "optable:", err)
		os.Exit(1)
	}
	fmt.Printf("optable: %d operations -> %s\n", n, *out)
}
