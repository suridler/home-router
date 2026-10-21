package domainstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
)

const pendingBaselineDDL = `CREATE TABLE IF NOT EXISTS geodata_pending_baseline (
 asset TEXT NOT NULL, digest TEXT NOT NULL, previous_runtime TEXT NOT NULL,
 resources TEXT NOT NULL, PRIMARY KEY(asset,digest)
)`

// StageBaseline records the exact set before the atomic file replacement. An
// interrupted write is harmless: activation requires the matching file hash
// and a different Xray runtime, never merely a running helper.
func (s *Store) StageBaseline(ctx context.Context, name, digest, token string, resources []string) error {
	if filepath.Base(name) != name || len(digest) != 64 || token == "" {
		return errors.New("invalid geodata baseline")
	}
	encoded, err := json.Marshal(resources)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO geodata_pending_baseline(asset,digest,previous_runtime,resources) VALUES(?,?,?,?)
 ON CONFLICT(asset,digest) DO UPDATE SET digest=excluded.digest,previous_runtime=excluded.previous_runtime,resources=excluded.resources`, name, digest, token, string(encoded))
	return err
}

// NeedsGeodataRestart recognizes a replacement interrupted before activation.
// A failed rename or a file restored after a failed test does not match its
// candidate hash and therefore never forces an unnecessary restart.
func (s *Store) NeedsGeodataRestart(ctx context.Context, token, assetDir string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT asset,digest FROM geodata_pending_baseline WHERE previous_runtime=?", token)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, digest string
		if err := rows.Scan(&name, &digest); err != nil {
			return false, err
		}
		actual, err := assetDigest(assetDir, name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if actual == digest {
			return true, nil
		}
	}
	return false, rows.Err()
}

func assetDigest(dir, name string) (string, error) {
	if name == "." || filepath.Base(name) != name {
		return "", errors.New("invalid pending asset")
	}
	file, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *Store) ActivateBaselines(ctx context.Context, token, assetDir string, activeAssets map[string]bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT asset,digest,previous_runtime,resources FROM geodata_pending_baseline ORDER BY asset")
	if err != nil {
		return err
	}
	type pending struct{ name, digest, previous, resources string }
	all := []pending{}
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.name, &p.digest, &p.previous, &p.resources); err != nil {
			rows.Close()
			return err
		}
		all = append(all, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range all {
		if token == p.previous {
			continue
		}
		digest, err := assetDigest(assetDir, p.name)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if digest != p.digest {
			// A candidate not installed (or explicitly replaced later) does not
			// describe the live baseline. Keep the existing baseline unchanged.
			if _, err := tx.ExecContext(ctx, "DELETE FROM geodata_pending_baseline WHERE asset=? AND digest=?", p.name, p.digest); err != nil {
				return err
			}
			continue
		}
		if p.name == "geosite.dat" || p.name == "geoip.dat" {
			if _, err := tx.ExecContext(ctx, "DELETE FROM geodata_pending_baseline WHERE asset=? AND digest=?", p.name, p.digest); err != nil {
				return err
			}
			continue
		}
		// Keep runtime additions until Xray actually uses the new file.
		if !activeAssets[p.name] {
			continue
		}
		var prepared []string
		if err := json.Unmarshal([]byte(p.resources), &prepared); err != nil {
			return err
		}
		ips := len(p.name) >= 6 && p.name[:6] == "geoip_"
		snapshot, err := snapshotTx(ctx, tx)
		if err != nil {
			return err
		}
		for _, value := range snapshot.Baseline {
			_, ipErr := netip.ParseAddr(value)
			if (ipErr == nil) != ips {
				continue
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM proxy_domain_state WHERE domain=? AND desired=0", value); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE proxy_domain_state SET baseline=0 WHERE domain=? AND desired=1", value); err != nil {
				return err
			}
		}
		for _, value := range prepared {
			if _, err := tx.ExecContext(ctx, `INSERT INTO proxy_domain_state(domain,desired,baseline) VALUES(?,0,1)
      ON CONFLICT(domain) DO UPDATE SET baseline=1`, value); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM geodata_pending_baseline WHERE asset=? AND digest=?", p.name, p.digest); err != nil {
			return err
		}
	}
	return tx.Commit()
}
