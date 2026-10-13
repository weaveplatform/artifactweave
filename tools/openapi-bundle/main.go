// Command openapi-bundle merges api/src/<service>/*.yaml fragments into
// api/<service>.openapi.yaml (make bundle). See internal/openapibundle.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/weaveplatform/weaveplatform-artifact-registry/internal/openapibundle"
)

func main() {
	src := flag.String("src", "api/src", "directory holding one sub-directory per service")
	out := flag.String("out", "api", "directory receiving <service>.openapi.yaml")
	flag.Parse()
	done, err := openapibundle.BundleAll(*src, *out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "openapi-bundle:", err)
		os.Exit(1)
	}
	for _, s := range done {
		fmt.Printf("bundled %s -> %s/%s.openapi.yaml\n", s, *out, s)
	}
}
