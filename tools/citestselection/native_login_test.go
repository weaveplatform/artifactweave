package main

import (
	"slices"
	"testing"
)

func TestUnitTerraformNativeLoginClientInputs(t *testing.T) {
	selected := formats["terraform"]
	if !slices.Contains(selected.clients, "node") || !slices.Contains(selected.clients, "terraform") {
		t.Fatal("native Terraform approval requires pinned client container and CLI", selected.clients)
	}
}
