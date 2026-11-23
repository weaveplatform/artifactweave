package main

import (
	"context"
	"fmt"
	"os"

	"github.com/weaveplatform/artifactweave/internal/devtools/configurationproof"
)

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: configurationproof <database> <proxy/<key>|mail_server/mailserver|ldap/<key>> <phase> <evidence-json>")
		os.Exit(2)
	}
	if err := configurationproof.Run(context.Background(), os.Args[1], os.Args[2], os.Args[3], os.Args[4],
		os.Getenv("CONFIGURATION_PROOF_MASTER_KEY"), os.Getenv("CONFIGURATION_PROOF_PASSWORD")); err != nil {
		fmt.Fprintln(os.Stderr, "configuration secret proof:", err)
		os.Exit(1)
	}
	fmt.Printf("configuration secret proof: %s PASS\n", os.Args[3])
}
