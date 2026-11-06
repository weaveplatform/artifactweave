package item

// NonUniqueSnapshot is nonUniqueSnapshot, for the parity test in package
// item_test. That test imports the Maven slice, and once the slice imports
// core/remote, which imports this package, an in-package test could not.
var NonUniqueSnapshot = nonUniqueSnapshot
