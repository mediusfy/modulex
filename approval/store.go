package approval

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FileStore persists Broker grants to a JSON file, bridging approvals
// across process boundaries — e.g. a human running a CLI grant command in
// one process, and a separately-running MCP server's Broker seeing it in
// another. This is the durable store the package doc's "No persistence"
// section anticipated as future work, "only worth building if a real
// CLI/MCP integration needs approvals to outlive a single process."
//
// FileStore holds no grants itself; every Save/Load round-trips through
// the file on disk, so two processes stay consistent via the filesystem
// alone, with no other IPC. Save is not safe for concurrent writers across
// processes (last write wins) — this targets the expected usage pattern of
// an infrequent, human-run grant, not concurrent automated writers.
type FileStore struct {
	path string
}

// DefaultStorePath returns the well-known FileStore path for a repository
// at root: <root>/.modulex/approvals.json. Shared by every caller that
// needs to agree on this exact path without duplicating it —
// tools/mcpserver's run_verification and tools/agentcli's `modulex agent
// approve` are separate Go modules/processes, and a grant only bridges
// between them if both resolve the same path for the same root.
func DefaultStorePath(root string) string {
	return filepath.Join(root, ".modulex", "approvals.json")
}

// NewFileStore returns a FileStore backed by path. The file and its parent
// directory are created on first Save if they don't exist yet.
func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

// storedGrant is FileStore's on-disk representation of a Grant. Unlike
// Grant's own JSON encoding (Token tagged json:"-"), Token must round-trip
// here — a grant that couldn't be redeemed after a save/load cycle would
// be useless. TokenHash is not stored; Load recomputes it via hashToken,
// so the two can never drift apart. Treat this file exactly like a
// credentials file (see the package doc's "Token sensitivity" section):
// Save writes it with mode 0o600.
type storedGrant struct {
	Token      string    `json:"token"`
	Scope      Scope     `json:"scope"`
	ApprovedBy string    `json:"approved_by"`
	ApprovedAt time.Time `json:"approved_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Used       bool      `json:"used"`
}

// Save writes every grant currently in b — active or not — to the store,
// replacing any previous content. Save does not filter expired/used
// grants itself; Load is where that happens, so a Save/Load round trip is
// lossless. Writes to a temp file and renames into place, so a concurrent
// Load never observes a partially-written file.
func (s *FileStore) Save(b *Broker) error {
	b.mu.Lock()
	grants := make([]storedGrant, 0, len(b.grants))
	for _, g := range b.grants {
		grants = append(grants, storedGrant{
			Token:      g.Token,
			Scope:      g.Scope,
			ApprovedBy: g.ApprovedBy,
			ApprovedAt: g.ApprovedAt,
			ExpiresAt:  g.ExpiresAt,
			Used:       g.Used,
		})
	}
	b.mu.Unlock()

	data, err := json.MarshalIndent(grants, "", "  ")
	if err != nil {
		return fmt.Errorf("approval: marshaling grants: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("approval: creating %s: %w", filepath.Dir(s.path), err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("approval: writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("approval: renaming %s to %s: %w", tmp, s.path, err)
	}
	return nil
}

// Load reads previously-saved grants from disk into a fresh Broker. A
// missing file is not an error: it returns an empty, zero-grant Broker,
// matching NewBroker's own "no elevated operation is approved by default"
// guarantee for a store nothing has ever written to. An expired or
// already-used grant is loaded exactly as saved, not dropped — Broker's
// existing expiry/single-use checks (Grant.expired, the Used flag) already
// treat it as denied, so Load does not need to duplicate that logic.
func (s *FileStore) Load() (*Broker, error) {
	b := NewBroker()

	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return nil, fmt.Errorf("approval: reading %s: %w", s.path, err)
	}

	var grants []storedGrant
	if err := json.Unmarshal(data, &grants); err != nil {
		return nil, fmt.Errorf("approval: parsing %s: %w", s.path, err)
	}

	b.mu.Lock()
	for _, sg := range grants {
		b.grants[sg.Token] = &Grant{
			Token:      sg.Token,
			TokenHash:  hashToken(sg.Token),
			Scope:      sg.Scope,
			ApprovedBy: sg.ApprovedBy,
			ApprovedAt: sg.ApprovedAt,
			ExpiresAt:  sg.ExpiresAt,
			Used:       sg.Used,
		}
	}
	b.mu.Unlock()

	return b, nil
}
