package smart

// The Smart registry helpers, for smart_registry_test.go: that test passes
// the Docker slice's parser, and an in-package test importing the slice is an
// import cycle.
var (
	SmartRegistryGrant = smartRegistryGrant
	SmartPropertyPath  = smartPropertyPath
	SmartStoragePath   = smartStoragePath
)
