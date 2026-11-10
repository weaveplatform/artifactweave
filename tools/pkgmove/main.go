package main

import (
	"os"

	"github.com/weaveplatform/artifactweave/internal/devtools/pkgmove"
)

func main() {
	os.Exit(pkgmove.Main(os.Args[1:], os.Stdout, os.Stderr))
}
