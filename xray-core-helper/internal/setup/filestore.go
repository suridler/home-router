package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"home-router/xray-core-helper/internal/geodatalimits"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/filemetadata"
	"home-router/xray-core-helper/internal/nativebinding"
	"home-router/xray-core-helper/internal/nativeblock"
	"home-router/xray-core-helper/internal/nativewrite"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/templatevalidation"
	"home-router/xray-core-helper/internal/xrayconfig"
	"home-router/xray-core-helper/internal/xrayidentity"
)

type validateFunc func(context.Context, templatecatalog.ID, string) error

const StateRoot = "/var/lib/xray-core-helper/setup"

type fileStore struct {
	maxAssetSize    int
	mu              sync.Mutex
	root            string
	entries         []templatecatalog.Entry
	validate        validateFunc
	now             func() time.Time
	fault           func(string, string) error
	metadata        func(templatecatalog.ID) (filemetadata.Metadata, error)
	workingMetadata func() filemetadata.Metadata
	preflight       *preflight
	identity        func(context.Context) (int, error)
	checkRelations  bool
	userGeosite     string
	userGeoip       string
	assetDir        string
	geositeBuilder  string
	configPath      string
	config          bootstrap.Config
	network         bootstrap.NetworkConfig
	validateAssets  func(context.Context, string, string) error
}
type setupManifest struct {
	Network     *setupFile  `json:"network,omitempty"`
	UserGeosite *setupAsset `json:"userGeosite,omitempty"`
	UserGeoip   *setupAsset `json:"userGeoip,omitempty"`
	Version     int         `json:"version"`
	ID          string      `json:"id"`
	Items       []setupFile `json:"items"`
}
type setupFile struct {
	Preserve      bool               `json:"preserve,omitempty"`
	ID            templatecatalog.ID `json:"templateId"`
	Target        string             `json:"target"`
	Candidate     string             `json:"candidate"`
	Backup        string             `json:"backup"`
	Existed       bool               `json:"existed"`
	Mode          uint32             `json:"mode"`
	UID           int                `json:"uid"`
	GID           int                `json:"gid"`
	Hash          string             `json:"hash"`
	CandidateHash string             `json:"candidateHash"`
	ApplyMode     uint32             `json:"applyMode"`
	ApplyUID      int                `json:"applyUid"`
	ApplyGID      int                `json:"applyGid"`
}
type fileTransaction struct {
	network  bootstrap.NetworkConfig
	drift    bool
	store    *fileStore
	manifest setupManifest
}

func NewFileStore(root string, validator interface {
	Validate(context.Context, templatecatalog.ID, string) (templatevalidation.Result, error)
}, assetDirs ...string) (SetupStore, error) {
	if validator == nil {
		return nil, errors.New("setup validator is required")
	}
	config, err := bootstrap.ReadDefaults(bootstrap.TemplateFile)
	if err != nil {
		return nil, err
	}
	return NewFileStoreWithCatalog(root, validator, templatecatalog.Default(), config, assetDirs...)
}

func NewFileStoreWithCatalog(root string, validator interface {
	Validate(context.Context, templatecatalog.ID, string) (templatevalidation.Result, error)
}, catalog templatecatalog.Catalog, config bootstrap.Config, assetDirs ...string) (SetupStore, error) {
	if validator == nil {
		return nil, errors.New("setup validator is required")
	}
	store, err := newFileStore(root, catalog.Entries(), config, func(ctx context.Context, id templatecatalog.ID, path string) error {
		_, err := validator.Validate(ctx, id, path)
		return err
	})
	if err != nil {
		return nil, err
	}
	if !geodatalimits.ValidMB(config.Geodata.MaxAssetSizeMB) {
		return nil, errors.New("invalid geodata size limit")
	}
	store.maxAssetSize = geodatalimits.Bytes(config.Geodata.MaxAssetSizeMB)
	store.metadata = filemetadata.Destination
	store.identity = xrayidentity.New().GID
	store.checkRelations = true
	assetDir := config.Xray.AssetDir
	if len(assetDirs) > 0 {
		assetDir = assetDirs[0]
	}
	if !filepath.IsAbs(assetDir) || filepath.Clean(assetDir) != assetDir {
		return nil, errors.New("invalid asset directory")
	}
	binary := config.Xray.Binary
	if len(assetDirs) > 1 {
		binary = assetDirs[1]
	}
	store.preflight = systemPreflight(assetDir, binary)
	if len(assetDirs) > 2 {
		if !bootstrap.ValidUserGeositeName(assetDirs[2]) {
			return nil, errors.New("invalid user geosite filename")
		}
		store.userGeosite = assetDirs[2]
	}
	builder := config.Geodata.Builder
	if len(assetDirs) > 3 {
		builder = assetDirs[3]
	}
	if len(assetDirs) > 4 {
		if !bootstrap.ValidUserGeoipName(assetDirs[4]) {
			return nil, errors.New("invalid user geoip filename")
		}
		store.userGeoip = assetDirs[4]
	}
	if err := store.configureGeosite(assetDir, binary, builder); err != nil {
		return nil, err
	}
	return store, nil
}
func newFileStore(root string, entries []templatecatalog.Entry, config bootstrap.Config, validate validateFunc) (*fileStore, error) {
	if !filepath.IsAbs(root) || len(entries) == 0 || validate == nil {
		return nil, errors.New("invalid setup file store")
	}
	return &fileStore{maxAssetSize: geodatalimits.Bytes(config.Geodata.MaxAssetSizeMB), root: root, entries: append([]templatecatalog.Entry(nil), entries...), validate: validate, now: time.Now, workingMetadata: filemetadata.Working, userGeosite: config.Geodata.UserGeosite, userGeoip: config.Geodata.UserGeoip, network: config.Network, config: config}, nil
}
func (s *fileStore) Prepare(ctx context.Context, plan Plan) (SetupTransaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkPlanReferences(plan); err != nil {
		return nil, err
	}
	plan.network = s.network
	plan.network.Interface, plan.network.Address, plan.network.IPv4Prefix = plan.Interface, plan.ListenAddress, plan.LANNetwork
	identityResolved := false
	resolveIdentity := func() error {
		if identityResolved || s.identity == nil {
			return nil
		}
		gid, err := s.identity(ctx)
		if err != nil {
			return &PreflightError{Code: "preflight_xray_identity_unavailable"}
		}
		plan.xrayGID, identityResolved = gid, true
		return nil
	}
	if err := safePath(s.root); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(s.pendingPath()); err == nil {
		return nil, ErrSetupBusy
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	id := s.now().UTC().Format("20060102T150405.000000000Z")
	dir := filepath.Join(s.root, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := syncDir(s.root); err != nil {
		return nil, err
	}
	m := setupManifest{Version: 1, ID: id}
	for _, entry := range s.entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		source := entry.WorkingPath
		if plan.PreserveXray && entry.ID == templatecatalog.XrayConfig {
			source = entry.SystemPath
		}
		raw, _, err := readSafe(source)
		if plan.PreserveXray && entry.ID == templatecatalog.XrayConfig && (plan.ConfigFingerprint == "" || contentHash(raw) != plan.ConfigFingerprint) {
			return nil, ErrRecoveryRequired
		}
		if err != nil {
			return nil, err
		}
		if entry.ID != templatecatalog.XrayConfig {
			sources, err := nativebinding.TemplateSources(raw)
			if err != nil {
				return nil, err
			}
			for _, source := range sources {
				if source == "xray.service-gid" {
					if err := resolveIdentity(); err != nil {
						return nil, err
					}
				}
			}
		}
		if plan.Gateway != nil && entry.ID == templatecatalog.XrayConfig {
			var analysis xrayconfig.Gateway
			raw, analysis, err = s.gatewayCandidate(plan)
			_ = analysis
			if err != nil || contentHash(raw) != plan.XrayCandidateHash {
				return nil, ErrRecoveryRequired
			}
		}
		if entry.ID == templatecatalog.XrayConfig {
			plan.xraySource = raw
		}
		raw, err = renderNative(entry.ID, raw, plan, s.userGeosite)
		if err != nil {
			return nil, err
		}
		nftPriorHash := ""
		nftExisted := false
		if entry.ID == templatecatalog.Nftables {
			prior, _, readErr := readSafe(entry.SystemPath)
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				return nil, readErr
			}
			nftExisted = readErr == nil
			nftPriorHash = contentHash(prior)
			merged, mergeErr := nativeblock.Build(prior, raw, true)
			if mergeErr != nil {
				return nil, mergeErr
			}
			raw = merged.Content
		}
		candidate := filepath.Join(dir, string(entry.ID)+".candidate"+filepath.Ext(entry.SystemPath))
		if err = s.replace(candidate, raw, 0600, os.Geteuid(), os.Getegid()); err != nil {
			return nil, err
		}
		item := setupFile{Preserve: plan.PreserveXray && entry.ID == templatecatalog.XrayConfig, ID: entry.ID, Target: entry.SystemPath, Candidate: candidate, CandidateHash: contentHash(raw), Backup: filepath.Join(dir, string(entry.ID)+".backup"), Mode: 0600, UID: os.Geteuid(), GID: os.Getegid()}
		if err = safePath(entry.SystemPath); err != nil {
			return nil, err
		}
		if old, info, e := readSafe(entry.SystemPath); e == nil {
			if entry.ID == templatecatalog.Nftables && (!nftExisted || contentHash(old) != nftPriorHash) {
				return nil, errors.New("setup native source changed")
			}
			item.Existed = true
			item.Mode = uint32(info.Mode().Perm())
			stat := info.Sys().(*syscall.Stat_t)
			item.UID, item.GID, item.Hash = int(stat.Uid), int(stat.Gid), contentHash(old)
			if e = s.replace(item.Backup, old, 0600, os.Geteuid(), os.Getegid()); e != nil {
				return nil, e
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return nil, e
		} else if entry.ID == templatecatalog.Nftables && nftExisted {
			return nil, errors.New("setup native source changed")
		}
		item.ApplyMode, item.ApplyUID, item.ApplyGID = item.Mode, item.UID, item.GID
		if s.metadata != nil {
			var metadata filemetadata.Metadata
			var err error
			if entry.ID == templatecatalog.XrayConfig && s.identity != nil {
				if err := resolveIdentity(); err != nil {
					return nil, err
				}
				metadata = filemetadata.Metadata{Mode: 0640, UID: 0, GID: plan.xrayGID}
			} else {
				metadata, err = s.metadata(entry.ID)
			}
			if err != nil {
				return nil, err
			}
			item.ApplyMode, item.ApplyUID, item.ApplyGID = uint32(metadata.Mode), metadata.UID, metadata.GID
		}
		m.Items = append(m.Items, item)
	}
	if plan.Gateway != nil {
		m.Version = 2
		for _, entry := range s.entries {
			// Every working file is an immutable render source, including Xray.
			raw, _, err := readSafe(entry.WorkingPath)
			if err != nil {
				return nil, err
			}
			metadata := s.workingMetadata()
			item := setupFile{ID: entry.ID, Target: entry.WorkingPath, Candidate: filepath.Join(dir, string(entry.ID)+".working.candidate"+filepath.Ext(entry.SystemPath)), Backup: filepath.Join(dir, string(entry.ID)+".working.backup"), CandidateHash: contentHash(raw), ApplyMode: uint32(metadata.Mode), ApplyUID: metadata.UID, ApplyGID: metadata.GID, Mode: 0600, UID: os.Geteuid(), GID: os.Getegid()}
			if err = s.replace(item.Candidate, raw, 0600, os.Geteuid(), os.Getegid()); err != nil {
				return nil, err
			}
			old, info, err := readSafe(entry.WorkingPath)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			if err == nil {
				item.Existed = true
				item.Hash = contentHash(old)
				item.Mode = uint32(info.Mode().Perm())
				st := info.Sys().(*syscall.Stat_t)
				item.UID, item.GID = int(st.Uid), int(st.Gid)
				if err = s.replace(item.Backup, old, 0600, os.Geteuid(), os.Getegid()); err != nil {
					return nil, err
				}
			}
			item.Preserve = true
			item.ApplyMode, item.ApplyUID, item.ApplyGID = item.Mode, item.UID, item.GID
			m.Items = append(m.Items, item)
		}
	}
	if plan.Gateway != nil && s.assetDir != "" {
		m.Version = 3
		if err := s.prepareGeosite(ctx, &m); err != nil {
			return nil, err
		}
		if err := s.prepareGeoip(&m); err != nil {
			return nil, err
		}
	}
	if err := s.prepareNetwork(&m, plan); err != nil {
		return nil, err
	}
	if err := s.checkPlanReferences(plan); err != nil {
		return nil, err
	}
	if err := s.writeManifest(m); err != nil {
		return nil, err
	}
	return &fileTransaction{store: s, manifest: m, network: plan.network}, nil
}
func (t *fileTransaction) Validate(ctx context.Context) error {
	for index, item := range t.manifest.files() {
		raw, _, err := readSafe(item.Candidate)
		if err != nil || contentHash(raw) != item.CandidateHash {
			return errors.New("setup candidate changed")
		}
		if item.Preserve && (item.ID != templatecatalog.XrayConfig || index >= len(t.store.entries)) {
			continue
		}
		if item.ID == networkID {
			if _, err := bootstrap.LoadWithTemplate(item.Candidate, bootstrap.TemplateFile); err != nil {
				return err
			}
		} else if item.ID == templatecatalog.XrayConfig {
			if err := t.store.validateWithUserAssets(ctx, t.manifest, item.Candidate); err != nil {
				return err
			}
		} else if err := t.store.validate(ctx, item.ID, item.Candidate); err != nil {
			return err
		}
	}
	if t.store.checkRelations {
		inputs, err := t.templateValues()
		if err != nil {
			return err
		}
		values := map[templatecatalog.ID][]byte{}
		for _, item := range t.manifest.files() {
			raw, _, err := readSafe(item.Candidate)
			if err != nil || contentHash(raw) != item.CandidateHash {
				return errors.New("setup candidate changed")
			}
			if item.ID != networkID {
				if _, exists := values[item.ID]; !exists {
					values[item.ID] = raw
				}
			}
		}
		documents := map[string]*nativebinding.Document{}
		for id, raw := range values {
			if id == templatecatalog.XrayConfig && !bytes.Contains(raw, []byte("// helper:")) {
				continue
			}
			doc, err := nativebinding.Parse(raw)
			if err != nil {
				return err
			}
			documents[string(id)] = doc
		}
		checks, err := nativebinding.CheckDocuments(documents, func(source string) (string, error) {
			if strings.HasPrefix(source, "helper.network.") {
				n, err := t.store.manifestNetwork(t.manifest)
				if t.manifest.Network == nil {
					n = t.network
				}
				if err != nil {
					return "", err
				}
				return n.BindingValue(source)
			}
			if strings.HasPrefix(source, "helper.") {
				return inputs.Resolve(source)
			}
			if source == "xray.service-gid" {
				if t.store.identity == nil {
					return "", nativebinding.ErrUnresolved
				}
				gid, err := t.store.identity(ctx)
				return fmt.Sprint(gid), err
			}
			return xrayconfig.BindingValue(values[templatecatalog.XrayConfig], source)
		})
		if err != nil {
			return err
		}
		for _, check := range checks {
			if check.Status == "unresolved" {
				return nativebinding.ErrUnresolved
			}
			if check.Status == "mismatch" {
				return nativebinding.ErrMismatch
			}
		}
	}
	return nil
}
func (t *fileTransaction) Commit(ctx context.Context) error {
	// Reject external file/metadata drift before replacing any member of the group.
	if err := t.store.checkUserAssets(t.manifest, true); err != nil {
		t.drift = true
		return err
	}
	for _, item := range t.manifest.files() {
		raw, info, err := readSafe(item.Target)
		if !item.Existed {
			if !errors.Is(err, os.ErrNotExist) {
				t.drift = true
				return errors.New("setup destination changed")
			}
			continue
		}
		if err != nil || contentHash(raw) != item.Hash {
			t.drift = true
			return errors.New("setup destination changed")
		}
		st := info.Sys().(*syscall.Stat_t)
		if uint32(info.Mode().Perm()) != item.Mode || int(st.Uid) != item.UID || int(st.Gid) != item.GID {
			t.drift = true
			return errors.New("setup destination metadata changed")
		}
	}

	if err := t.store.publishUserAssets(t.manifest); err != nil {
		if errors.Is(err, ErrRecoveryRequired) {
			t.drift = true
		}
		return err
	}
	for _, item := range t.manifest.files() {
		if item.Preserve {
			raw, _, err := readSafe(item.Target)
			if err != nil || contentHash(raw) != item.CandidateHash {
				return errors.New("preserved setup file changed")
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		raw, _, err := readSafe(item.Candidate)
		if err != nil {
			return err
		}
		if contentHash(raw) != item.CandidateHash {
			return errors.New("setup candidate changed")
		}
		if err = t.store.replace(item.Target, raw, os.FileMode(item.ApplyMode), item.ApplyUID, item.ApplyGID); err != nil {
			return err
		}
	}
	return nil
}
func (t *fileTransaction) Rollback(context.Context) error {
	if t.drift {
		return ErrRecoveryRequired
	}
	return t.store.restore(t.manifest)
}
func (t *fileTransaction) Complete(context.Context) error { return t.store.clearPending() }
func (s *fileStore) Recover(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, _, err := readSafe(s.pendingPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var m setupManifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m) != nil || s.validateManifest(m) != nil {
		return errors.New("invalid setup recovery manifest")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("invalid setup recovery manifest")
	}
	return s.restore(m)
}
func (s *fileStore) restore(m setupManifest) error {
	if err := s.validateManifest(m); err != nil {
		return err
	}
	// Verify all evidence before restoring even the first target.
	if err := s.checkUserAssets(m, true); err != nil {
		return err
	}
	backups := make(map[string][]byte)
	for _, item := range m.files() {
		if err := safePath(item.Target); err != nil {
			return err
		}
		if item.Existed {
			raw, _, err := readSafe(item.Backup)
			if err != nil {
				return err
			}
			if contentHash(raw) != item.Hash {
				return errors.New("setup backup hash mismatch")
			}
			backups[item.Backup] = raw
		}
	}
	var result error
	files := m.files()
	for i := len(files) - 1; i >= 0; i-- {
		item := files[i]
		if item.Preserve {
			continue
		}
		if item.Existed {
			err := s.replace(item.Target, backups[item.Backup], os.FileMode(item.Mode), item.UID, item.GID)
			result = errors.Join(result, err)
		} else {
			var err error
			if item.Target == nativewrite.Target {
				err = nativewrite.Remove(context.Background())
			} else {
				err = os.Remove(item.Target)
			}
			if !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, err)
			}
			if err == nil {
				result = errors.Join(result, syncDir(filepath.Dir(item.Target)))
			}
		}
	}
	if result == nil {
		result = s.restoreUserAssets(m)
	}
	if result == nil && m.Network != nil {
		c, err := bootstrap.Load(s.configPath)
		result = err
		if err == nil {
			s.network = c.Network
		}
	}
	if result == nil {
		result = s.clearPending()
	}
	return result
}
func (s *fileStore) pendingPath() string { return filepath.Join(s.root, "pending.json") }
func (s *fileStore) writeManifest(m setupManifest) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(s.root, 0700); err != nil {
		return err
	}
	if err = s.replace(filepath.Join(s.root, m.ID, "manifest.json"), append(raw, '\n'), 0600, os.Geteuid(), os.Getegid()); err != nil {
		return err
	}
	return s.replace(s.pendingPath(), append(raw, '\n'), 0600, os.Geteuid(), os.Getegid())
}

func (s *fileStore) validateManifest(m setupManifest) error {
	if err := s.validateNetworkManifest(m); err != nil {
		return err
	}
	if err := s.validateAssetManifest(m); err != nil {
		return err
	}
	if (m.Version != 1 && m.Version != 2 && m.Version != 3 && m.Version != 4 && m.Version != 5) || m.ID == "" || filepath.Base(m.ID) != m.ID || strings.Contains(m.ID, "..") || (m.Version == 1 && len(m.Items) != len(s.entries)) || (m.Version >= 2 && len(m.Items) != 2*len(s.entries)) {
		return errors.New("invalid setup manifest")
	}
	if err := safePath(filepath.Join(s.root, m.ID)); err != nil {
		return err
	}
	for i, item := range m.Items {
		entry := s.entries[i%len(s.entries)]
		target := entry.SystemPath
		stem := string(entry.ID)
		if i >= len(s.entries) {
			target = entry.WorkingPath
			stem += ".working"

		}
		if item.Preserve && (!item.Existed || item.Hash != item.CandidateHash || i < len(s.entries) && item.ID != templatecatalog.XrayConfig) {
			return errors.New("invalid preserved config")
		}
		if item.ApplyMode == 0 || item.ApplyMode & ^uint32(0777) != 0 || item.ApplyUID < 0 || item.ApplyGID < 0 {
			return errors.New("invalid destination metadata")
		}
		dir := filepath.Join(s.root, m.ID)
		oldCandidate := filepath.Join(dir, stem+".candidate")
		candidate := oldCandidate + filepath.Ext(entry.SystemPath)
		// Keep recovery of already durable v1 evidence possible; both names are
		// bound to this catalog entry and transaction, never caller-supplied paths.
		if item.ID != entry.ID || item.Target != target || (item.Candidate != candidate && item.Candidate != oldCandidate) || item.Backup != filepath.Join(dir, stem+".backup") || item.Mode & ^uint32(0777) != 0 || item.UID < 0 || item.GID < 0 || len(item.CandidateHash) != 64 || (item.Existed && len(item.Hash) != 64) {
			return errors.New("invalid setup manifest mapping")
		}
	}
	return nil
}

func contentHash(raw []byte) string { hash := sha256.Sum256(raw); return hex.EncodeToString(hash[:]) }

func (s *fileStore) clearPending() error {
	if err := s.fail("remove", s.pendingPath()); err != nil {
		return err
	}
	if err := os.Remove(s.pendingPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDir(s.root)
}

func (s *fileStore) fail(stage, path string) error {
	if s.fault != nil {
		return s.fault(stage, path)
	}
	return nil
}
func renderNative(id templatecatalog.ID, raw []byte, plan Plan, userGeosite ...string) ([]byte, error) {
	if id == templatecatalog.XrayConfig {
		if plan.PreserveXray && plan.Gateway == nil {
			return append([]byte(nil), raw...), nil
		}
		name := ""
		if len(userGeosite) > 0 {
			name = userGeosite[0]
		}
		return xrayconfig.BindUserGeosite(raw, name)
	}
	resolve := func(source string) (string, error) {
		switch source {
		case "setup.interface":
			return plan.Interface, nil
		case "setup.ipv4-prefix":
			return plan.LANNetwork, nil
		case "xray.service-gid":
			return fmt.Sprint(plan.xrayGID), nil
		case "xray.tproxy-port":
			if plan.Gateway != nil {
				return fmt.Sprint(plan.Gateway.Port), nil
			}
		case "xray.egress-mark":
			if plan.Gateway != nil {
				return fmt.Sprint(plan.Gateway.EgressMark), nil
			}
		default:
			n := plan.network
			n.Interface, n.Address, n.IPv4Prefix = plan.Interface, plan.ListenAddress, plan.LANNetwork
			return n.BindingValue(source)
		}
		return xrayconfig.BindingValue(plan.xraySource, source)
	}
	prepared, err := nativebinding.Prepare(raw, resolve)
	if err != nil {
		return nil, err
	}
	document, err := nativebinding.Parse(prepared)
	if err != nil {
		return nil, err
	}
	return document.RenderSelected(resolve, func(binding nativebinding.Binding) bool {
		return strings.HasPrefix(binding.Source, "setup.") || strings.HasPrefix(binding.Source, "helper.network.") || binding.Source == "xray.service-gid" || plan.Gateway != nil && (binding.Source == "xray.tproxy-port" || binding.Source == "xray.egress-mark")
	})
}
func writePrivate(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0600)
}
func atomicReplace(path string, raw []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".setup-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err = temp.Chmod(mode); err == nil {
		_, err = temp.Write(raw)
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
