package opkg

import (
	"bytes"
	"os"
	"testing"
)

// This archive is OpenWrt 24.10.0 x86_64/base/464xlat_13_x86_64.ipk,
// SHA256 3dcda4329728a2b3961083f49daa74be003827f790dc0c5fe36493e5fbd8b9b4.
func TestReadOpenWrtIPK(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("testdata/464xlat_13_x86_64.ipk")
	if err != nil {
		t.Fatal(err)
	}
	if got := Digest(
		body,
	); got != "3dcda4329728a2b3961083f49daa74be003827f790dc0c5fe36493e5fbd8b9b4" {
		t.Fatalf("upstream package changed: %s", got)
	}
	pkg, err := ReadPackage(t.Context(), bytes.NewReader(body), int64(len(body)),
		"feed/464xlat_13_x86_64.ipk")
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Name != "464xlat" || pkg.Version != "13" || pkg.Architecture != "x86_64" ||
		pkg.Depends == "" || pkg.Size != int64(len(body)) {
		t.Fatalf("unexpected OpenWrt control: %+v", pkg)
	}
}

func TestRejectCorruptedOpenWrtGzipTrailer(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("testdata/464xlat_13_x86_64.ipk")
	if err != nil {
		t.Fatal(err)
	}
	body[len(body)-8] ^= 0xff // outer gzip CRC; tar contents are otherwise intact
	if _, err := ReadPackage(t.Context(), bytes.NewReader(body), int64(len(body)),
		"feed/464xlat_13_x86_64.ipk"); err == nil {
		t.Fatal("corrupt IPK gzip checksum was accepted")
	}
}
