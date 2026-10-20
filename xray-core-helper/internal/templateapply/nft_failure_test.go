package templateapply

import (
	"context"
	"errors"
	"home-router/xray-core-helper/internal/nativeblock"
	"os"
	"path/filepath"
	"testing"
)

type rejectingNft struct{ calls int }

func (r *rejectingNft) Run(_ context.Context, _ string, _ []string) error {
	r.calls++
	return errors.New("rejected")
}
func TestRejectedBatchNeverLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate")
	os.WriteFile(path, []byte(nativeblock.Begin+"\ntable inet custom {}\n"+nativeblock.End+"\n"), 0600)
	runner := &rejectingNft{}
	tx := commandRuntimeTransaction{runner: runner}
	if tx.applyNft(context.Background(), path) == nil || runner.calls != 1 {
		t.Fatal("load attempted after rejection")
	}
}
