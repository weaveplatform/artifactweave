// Command stubgen writes internal/server/gen/<service>/unimplemented.gen.go
// from the generated strict server interface (make generate). See
// internal/stubgen.
package main

import (
	"fmt"
	"os"

	"github.com/weaveplatform/weaveplatform-artifact-registry/internal/stubgen"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: stubgen <path/to/server.gen.go>...")
		os.Exit(2)
	}
	for _, p := range os.Args[1:] {
		n, err := stubgen.Run(p, "")
		if err != nil {
			fmt.Fprintln(os.Stderr, "stubgen:", err)
			os.Exit(1)
		}
		fmt.Printf("stubgen: %d operations stubbed for %s\n", n, p)
	}
}
