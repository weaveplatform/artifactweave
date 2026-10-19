package repo

import "strings"

// displayNames holds the package types whose display name differs from
// their listing name. The storage summary sends the display name, and 7.x
// captures of it show npm repositories as "npm" and rpm repositories as
// "RPM", where the repositories list shows "Npm" and "YUM". Every other type
// they show is spelled as the list spells it. The sources are in research
// 03, "Package type names in listings".
var displayNames = map[PackageType]string{
	NPM: "npm",
	RPM: "RPM",
}

// PackageTypeDisplayName returns the spelling of packageType that Artifactory
// 7.x's storage summary (GET /api/storageinfo) shows, such as "Generic",
// "npm" or "RPM". jfrog-cli-core's transfer-files compares it with "Maven",
// "Gradle", "Ivy", "Docker", "Conan", "NuGet" and "SBT" to order uploads.
// The lookup ignores case, and a type with no display name of its own is
// spelled as PackageTypeName spells it.
func PackageTypeDisplayName(packageType string) string {
	if name, ok := displayNames[PackageType(strings.ToLower(packageType))]; ok {
		return name
	}
	return PackageTypeName(packageType)
}
