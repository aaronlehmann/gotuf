// tuf defines the core TUF logic around manipulating a repo.
package tuf

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/endophage/gotuf/data"
	"github.com/endophage/gotuf/errors"
	"github.com/endophage/gotuf/keys"
	"github.com/endophage/gotuf/signed"
	"github.com/endophage/gotuf/utils"
)

type ErrSigVerifyFail struct{}

func (e ErrSigVerifyFail) Error() string {
	return "Error: Signature verification failed"
}

type ErrMetaExpired struct{}

func (e ErrMetaExpired) Error() string {
	return "Error: Metadata has expired"
}

type ErrLocalRootExpired struct{}

func (e ErrLocalRootExpired) Error() string {
	return "Error: Local Root Has Expired"
}

// TufRepo is an in memory representation of the TUF Repo.
// It operates at the data.Signed level, accepting and producing
// data.Signed objects. Users of a TufRepo are responsible for
// fetching raw JSON and using the Set* functions to populate
// the TufRepo instance.
type TufRepo struct {
	Root      *data.SignedRoot
	Targets   map[string]*data.SignedTargets
	Snapshot  *data.SignedSnapshot
	Timestamp *data.SignedTimestamp
	keysDB    *keys.KeyDB
	signer    *signed.Signer
}

// NewTufRepo initializes a TufRepo instance with a keysDB and a signer.
// If the TufRepo will only be used for reading, the signer should be nil.
func NewTufRepo(keysDB *keys.KeyDB, signer *signed.Signer) *TufRepo {
	repo := &TufRepo{
		Targets: make(map[string]*data.SignedTargets),
		keysDB:  keysDB,
		signer:  signer,
	}
	return repo
}

// AddKeys adds the provided keys to the given role. If the role is
// a delegated targets role, the appropriate targets file that contains
// the delegation will be found and modified.
func (tr *TufRepo) AddKeys(role string, keys ...data.Key) error {
	return nil
}

// RemoveKeys deletes the keyIDs provided from the given role. If
// no other roles in the file reference the removed IDs, the key
// entries will also be removed.
func (tr *TufRepo) RemoveKeys(role string, keyIDs ...string) error {
	return nil
}

// UpdateDelegations updates the appropriate delegations, either adding
// a new delegation or updating an existing one. If keys are
// provided, the IDs will be added to the role (if they do not exist
// there already), and the keys will be added to the targets file.
// The "before" argument specifies another role which this new role
// will be added in front of (i.e. higher priority) in the delegation list.
// An empty before string indicates to add the role to the end of the
// delegation list.
// A new, empty, targets file will be created for the new role.
func (tr *TufRepo) UpdateDelegations(role *data.Role, keys []data.Key, before string) error {
	if !role.IsDelegation() || !role.IsValid() {
		return errors.ErrInvalidRole{}
	}
	parent := filepath.Dir(role.Name)
	p, ok := tr.Targets[parent]
	if !ok {
		return errors.ErrInvalidRole{}
	}
	for _, k := range keys {
		if !utils.StrSliceContains(role.KeyIDs, k.ID()) {
			role.KeyIDs = append(role.KeyIDs, k.ID())
		}
		key := data.NewPublicKey(k.Cipher(), k.Public())
		p.Signed.Delegations.Keys[k.ID()] = &key.TUFKey
		tr.keysDB.AddKey(key)
	}

	i := -1
	var r *data.Role
	for i, r = range p.Signed.Delegations.Roles {
		if r.Name == role.Name {
			break
		}
	}
	if i >= 0 {
		p.Signed.Delegations.Roles[i] = role
	} else {
		p.Signed.Delegations.Roles = append(p.Signed.Delegations.Roles, role)
	}
	p.Dirty = true

	roleTargets := data.NewTargets()
	tr.Targets[role.Name] = roleTargets

	tr.keysDB.AddRole(role)

	return nil
}

// InitRepo creates the base files for a repo. It inspects data.ValidRoles and
// data.ValidTypes to determine what the role names and filename should be. It
// also relies on the keysDB having already been populated with the keys and
// roles.
func (tr *TufRepo) InitRepo(consistent bool) error {
	rootRoles := make(map[string]*data.RootRole)
	rootKeys := make(map[string]*data.TUFKey)
	for _, r := range data.ValidRoles {
		role := tr.keysDB.GetRole(r)
		if role == nil {
			return errors.ErrInvalidRole{}
		}
		rootRoles[r] = &role.RootRole
		for _, kid := range role.KeyIDs {
			// don't need to check if GetKey returns nil, Key presence was
			// checked by KeyDB when role was added.
			key := tr.keysDB.GetKey(kid)
			// Create new key object to doubly ensure private key is excluded
			k := data.NewTUFKey(key.Cipher(), key.Public(), "")
			rootKeys[kid] = k
		}
	}
	root, err := data.NewRoot(rootKeys, rootRoles, consistent)
	if err != nil {
		return err
	}
	tr.Root = root

	targets := data.NewTargets()
	tr.Targets[data.ValidRoles["targets"]] = targets

	signedRoot, err := tr.SignRoot(data.DefaultExpires("root"))
	if err != nil {
		return err
	}
	signedTargets, err := tr.SignTargets("targets", data.DefaultExpires("targets"))
	if err != nil {
		return err
	}
	snapshot, err := data.NewSnapshot(signedRoot, signedTargets)
	if err != nil {
		return err
	}
	tr.Snapshot = snapshot

	signedSnapshot, err := tr.SignSnapshot(data.DefaultExpires("snapshot"))
	if err != nil {
		return err
	}
	timestamp, err := data.NewTimestamp(signedSnapshot)
	if err != nil {
		return err
	}

	tr.Timestamp = timestamp
	return nil
}

// SetRoot parses the Signed object into a SignedRoot object, sets
// the keys and roles in the KeyDB, and sets the TufRepo.Root field
// to the SignedRoot object.
func (tr *TufRepo) SetRoot(s *data.Signed) error {
	r, err := data.RootFromSigned(s)
	if err != nil {
		return err
	}
	for kid, key := range r.Signed.Keys {
		tr.keysDB.AddKey(&data.PublicKey{TUFKey: *key})
		logrus.Debug("Given Key ID:", kid, "\nGenerated Key ID:", key.ID())
	}
	for roleName, role := range r.Signed.Roles {
		roleName = strings.TrimSuffix(roleName, ".txt")
		rol, err := data.NewRole(
			roleName,
			role.Threshold,
			role.KeyIDs,
			nil,
			nil,
		)
		if err != nil {
			return err
		}
		err = tr.keysDB.AddRole(rol)
		if err != nil {
			return err
		}
	}
	tr.Root = r
	return nil
}

// SetTimestamp parses the Signed object into a SignedTimestamp object
// and sets the TufRepo.Timestamp field.
func (tr *TufRepo) SetTimestamp(s *data.Signed) error {
	ts, err := data.TimestampFromSigned(s)
	if err != nil {
		return err
	}
	tr.Timestamp = ts
	return nil
}

// SetSnapshot parses the Signed object into a SignedSnapshots object
// and sets the TufRepo.Snapshot field.
func (tr *TufRepo) SetSnapshot(s *data.Signed) error {
	snap, err := data.SnapshotFromSigned(s)
	if err != nil {
		return err
	}

	tr.Snapshot = snap
	return nil
}

// SetTargets parses the Signed object into a SignedTargets object,
// reads the delegated roles and keys into the KeyDB, and sets the
// SignedTargets object agaist the role in the TufRepo.Targets map.
func (tr *TufRepo) SetTargets(role string, s *data.Signed) error {
	t, err := data.TargetsFromSigned(s)
	if err != nil {
		return err
	}
	for _, k := range t.Signed.Delegations.Keys {
		tr.keysDB.AddKey(&data.PublicKey{TUFKey: *k})
	}
	for _, r := range t.Signed.Delegations.Roles {
		tr.keysDB.AddRole(r)
	}
	tr.Targets[role] = t
	return nil
}

// TargetMeta returns the FileMeta entry for the given path in the
// targets file associated with the given role. This may be nil if
// the target isn't found in the targets file.
func (tr TufRepo) TargetMeta(role, path string) *data.FileMeta {
	if t, ok := tr.Targets[role]; ok {
		if m, ok := t.Signed.Targets[path]; ok {
			return &m
		}
	}
	return nil
}

// TargetDelegations returns a slice of Roles that are valid publishers
// for the target path provided.
func (tr TufRepo) TargetDelegations(role, path, pathHex string) []*data.Role {
	if pathHex == "" {
		pathDigest := sha256.Sum256([]byte(path))
		pathHex = hex.EncodeToString(pathDigest[:])
	}
	roles := make([]*data.Role, 0)
	if t, ok := tr.Targets[role]; ok {
		for _, r := range t.Signed.Delegations.Roles {
			if r.CheckPrefixes(pathHex) || r.CheckPaths(path) {
				roles = append(roles, r)
			}
		}
	}
	return roles
}

// FindTarget attempts to find the target represented by the given
// path by starting at the top targets file and traversing
// appropriate delegations until the first entry is found or it
// runs out of locations to search.
// N.B. Multiple entries may exist in different delegated roles
//      for the same target. Only the first one encountered is returned.
func (tr TufRepo) FindTarget(path string) *data.FileMeta {
	pathDigest := sha256.Sum256([]byte(path))
	pathHex := hex.EncodeToString(pathDigest[:])

	var walkTargets func(role string) *data.FileMeta
	walkTargets = func(role string) *data.FileMeta {
		if m := tr.TargetMeta(role, path); m != nil {
			return m
		}
		// Depth first search of delegations based on order
		// as presented in current targets file for role:
		for _, r := range tr.TargetDelegations(role, path, pathHex) {
			if m := walkTargets(r.Name); m != nil {
				return m
			}
		}
		return nil
	}

	return walkTargets("targets")
}

// AddTargetsToRole will attempt to add the given targets specifically to
// the directed role. If the user does not have the signing keys for the role
// the function will return an error and the full slice of targets.
func (tr *TufRepo) AddTargets(role string, targets *data.Files) (*data.Files, error) {
	return nil, nil
}

func (tr *TufRepo) SignRoot(expires time.Time) (*data.Signed, error) {
	signed, err := tr.Root.ToSigned()
	if err != nil {
		return nil, err
	}
	root := tr.keysDB.GetRole(data.ValidRoles["root"])
	signed, err = tr.sign(signed, *root)
	if err != nil {
		return nil, err
	}
	tr.Root.Signatures = signed.Signatures
	return signed, nil
}

func (tr *TufRepo) SignTargets(role string, expires time.Time) (*data.Signed, error) {
	signed, err := tr.Targets[role].ToSigned()
	if err != nil {
		return nil, err
	}
	targets := tr.keysDB.GetRole(role)
	signed, err = tr.sign(signed, *targets)
	if err != nil {
		return nil, err
	}
	tr.Targets[role].Signatures = signed.Signatures
	return signed, nil
}

func (tr *TufRepo) SignSnapshot(expires time.Time) (*data.Signed, error) {
	signed, err := tr.Snapshot.ToSigned()
	if err != nil {
		return nil, err
	}
	snapshot := tr.keysDB.GetRole(data.ValidRoles["snapshot"])
	signed, err = tr.sign(signed, *snapshot)
	if err != nil {
		return nil, err
	}
	tr.Snapshot.Signatures = signed.Signatures
	return signed, nil
}

func (tr *TufRepo) SignTimestamp(expires time.Time) (*data.Signed, error) {
	signed, err := tr.Timestamp.ToSigned()
	if err != nil {
		return nil, err
	}
	timestamp := tr.keysDB.GetRole(data.ValidRoles["timestamp"])
	signed, err = tr.sign(signed, *timestamp)
	if err != nil {
		return nil, err
	}
	tr.Timestamp.Signatures = signed.Signatures
	return signed, nil
}

func (tr TufRepo) sign(signed *data.Signed, role data.Role) (*data.Signed, error) {
	ks := make([]*data.PublicKey, 0, len(role.KeyIDs))
	for _, kid := range role.KeyIDs {
		k := tr.keysDB.GetKey(kid)
		if k == nil {
			continue
		}
		ks = append(ks, k)
	}
	if len(ks) < 1 {
		return nil, keys.ErrInvalidKey
	}
	err := tr.signer.Sign(signed, ks...)
	if err != nil {
		return nil, err
	}
	return signed, nil
}
