package transaction

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
)

var ErrRollbackUnavailable = errors.New("previous accepted configuration is unavailable")

// RollbackStatus contains no configuration content or credentials.
type RollbackStatus struct {
	Available  bool   `json:"available"`
	Generation uint64 `json:"generation"`
}

// Accepted versions are private helper-created files, separate from templates.
func (engine *Engine) acceptedPath(hash string) string {
	return filepath.Join(filepath.Dir(engine.configPath), "."+filepath.Base(engine.configPath)+".helper-history", hash)
}

func (engine *Engine) saveAccepted(snapshot fileSnapshot) error {
	path := engine.acceptedPath(snapshot.hash)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if existing, err := readSnapshot(path); err == nil && existing.hash == snapshot.hash {
		return nil
	}
	// Accepted copies contain credentials. Keep new artifacts private to helper.
	snapshot.mode, snapshot.uid, snapshot.group = 0600, os.Geteuid(), os.Getegid()
	return restoreSnapshot(path, snapshot)
}

func (engine *Engine) previousAccepted(ctx context.Context) (fileSnapshot, error) {
	metadata, err := engine.store.Metadata(ctx)
	if err != nil {
		return fileSnapshot{}, err
	}
	hash, err := engine.store.RollbackHash(ctx, metadata.ContentHash)
	if err != nil {
		return fileSnapshot{}, err
	}
	current, err := readSnapshot(engine.configPath)
	if err != nil {
		return fileSnapshot{}, err
	}
	if current.hash != metadata.ContentHash {
		hash = metadata.ContentHash
	}
	if len(hash) != 64 || strings.Trim(hash, "0123456789abcdef") != "" {
		return fileSnapshot{}, ErrRollbackUnavailable
	}
	snapshot, err := readSnapshot(engine.acceptedPath(hash))
	if err != nil || snapshot.hash != hash {
		return fileSnapshot{}, ErrRollbackUnavailable
	}
	return snapshot, nil
}

func (engine *Engine) RollbackStatus(ctx context.Context) (RollbackStatus, error) {
	metadata, err := engine.store.Metadata(ctx)
	if err != nil {
		return RollbackStatus{}, err
	}
	_, err = engine.previousAccepted(ctx)
	if err != nil && !errors.Is(err, ErrRollbackUnavailable) {
		return RollbackStatus{}, err
	}
	return RollbackStatus{Available: err == nil, Generation: metadata.Generation}, nil
}

// Rollback uses the normal validated transaction and its restart-failure
// recovery. The snapshot is selected inside its mutation lock.
func (engine *Engine) Rollback(ctx context.Context, generation uint64) (Metadata, error) {
	log.Print("xray config rollback requested")
	metadata, err := engine.mutateOptions(ctx, generation, func(source []byte) ([]ByteEdit, error) {
		snapshot, err := engine.previousAccepted(ctx)
		if err != nil {
			return nil, err
		}
		return []ByteEdit{{Start: 0, End: len(source), Replacement: snapshot.content}}, nil
	}, true, nil, nil, true)
	if err != nil {
		code, _, _ := ErrorInfo(err)
		log.Printf("xray config rollback failed; code=%s", code)
		return Metadata{}, err
	}
	log.Printf("xray config rolled back and restarted; generation=%d", metadata.Generation)
	return metadata, nil
}

// Keep only the accepted current and previous versions. Pending transaction
// recovery has separate artifacts; these are never removed here.
func (engine *Engine) pruneAccepted(ctx context.Context) {
	metadata, err := engine.store.Metadata(ctx)
	if err != nil {
		return
	}
	previous, err := engine.store.RollbackHash(ctx, metadata.ContentHash)
	if err != nil {
		return
	}
	historyDir := filepath.Dir(engine.acceptedPath(metadata.ContentHash))
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		hash := name
		if len(hash) != 64 || strings.Trim(hash, "0123456789abcdef") != "" || hash == metadata.ContentHash || hash == previous {
			continue
		}
		if err := os.Remove(filepath.Join(historyDir, name)); err != nil {
			log.Print("xray config history cleanup failed")
		}
	}
}
