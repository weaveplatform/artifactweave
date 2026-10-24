package opkg

import (
	"os"
	"testing"
)

// The first two records of OpenWrt 24.10.0 x86_64/base/Packages check the
// native field spelling, multiline descriptions and content-addressed IPKs.
func TestDecodeOpenWrtPackages(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("testdata/openwrt-Packages")
	if err != nil {
		t.Fatal(err)
	}
	packages, err := DecodeIndex(body, "feeds/x86_64/base/Packages")
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 2 || packages[0].Name != "464xlat" ||
		packages[0].Path != "feeds/x86_64/base/464xlat_13_x86_64.ipk" ||
		packages[0].SHA256 != DigestMustOpenWrt() ||
		packages[1].Name != "6in4" {
		t.Fatalf("unexpected feed records: %+v", packages)
	}
}

func DigestMustOpenWrt() string {
	return "3dcda4329728a2b3961083f49daa74be003827f790dc0c5fe36493e5fbd8b9b4"
}
