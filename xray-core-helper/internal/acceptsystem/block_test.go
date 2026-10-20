package acceptsystem

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"home-router/xray-core-helper/internal/nativeblock"
	"home-router/xray-core-helper/internal/templatecatalog"
)

func TestAcceptNftCopiesOnlyManagedContents(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "system"), filepath.Join(dir, "template")
	block := func(body string) string { return nativeblock.Begin + "\n" + body + "\n" + nativeblock.End + "\n" }
	prior := "# template header\n" + block("table ip old {}") + "# template footer\n"
	os.WriteFile(source, []byte("# private host text\n"+block("table ip custom {}")+"# private host footer\n"), 0600)
	os.WriteFile(target, []byte(prior), 0600)
	c, err := (osPreparer{}).Prepare(templatecatalog.Nftables, source, target)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sum := sha256.Sum256([]byte(prior))
	if err = c.Commit(hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(target)
	if string(raw) != "# template header\n"+block("table ip custom {}")+"# template footer\n" {
		t.Fatal("unmanaged system content imported")
	}
}
