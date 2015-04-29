package signed

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	//"github.com/agl/ed25519"
	"github.com/endophage/go-tuf/data"
	"github.com/endophage/go-tuf/keys"
	"github.com/tent/canonical-json-go"
)

var (
	ErrMissingKey    = errors.New("tuf: missing key")
	ErrNoSignatures  = errors.New("tuf: data has no signatures")
	ErrInvalid       = errors.New("tuf: signature verification failed")
	ErrWrongMethod   = errors.New("tuf: invalid signature type")
	ErrUnknownRole   = errors.New("tuf: unknown role")
	ErrRoleThreshold = errors.New("tuf: valid signatures did not meet threshold")
	ErrWrongType     = errors.New("tuf: meta file has wrong type")
)

type signedMeta struct {
	Type    string    `json:"_type"`
	Expires time.Time `json:"expires"`
	Version int       `json:"version"`
}

type verificationService struct {
	verifiers map[string]Verifier
}

func NewVerificationService(verifier map[string]Verifier) verificationService {
	return verificationService{verifiers: verifiers}
}

func (vs verificationService) Verify(s *data.Signed, role string, minVersion int, db *keys.DB) error {
	if err := VerifySignatures(s, role, db); err != nil {
		return err
	}

	sm := &signedMeta{}
	if err := json.Unmarshal(s.Signed, sm); err != nil {
		return err
	}
	if strings.ToLower(sm.Type) != strings.ToLower(role) {
		return ErrWrongType
	}
	if IsExpired(sm.Expires) {
		return ErrExpired{sm.Expires}
	}
	if sm.Version < minVersion {
		return ErrLowVersion{sm.Version, minVersion}
	}

	return nil
}

var IsExpired = func(t time.Time) bool {
	return t.Sub(time.Now()) <= 0
}

func (vs verificationService) VerifySignatures(s *data.Signed, role string, db *keys.DB) error {
	if len(s.Signatures) == 0 {
		return ErrNoSignatures
	}

	fmt.Println("Role:", role)
	roleData := db.GetRole(role)
	if roleData == nil {
		return ErrUnknownRole
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(s.Signed, &decoded); err != nil {
		return err
	}
	msg, err := cjson.Marshal(decoded)
	if err != nil {
		return err
	}

	valid := make(map[string]struct{})
	for _, sig := range s.Signatures {
		if !roleData.ValidKey(sig.KeyID) {
			log.Printf("continuing b/c keyid was invalid: %s for roledata %s\n", sig.KeyID, roleData)
			continue
		}
		key := db.GetKey(sig.KeyID)
		if key == nil {
			log.Printf("continuing b/c keyid lookup was nil: %s\n", sig.KeyID)
			continue
		}

		verifier, ok := vs.verifiers[sig.Method]
		if !ok {
			log.Printf("continuing b/c signing method is not supported: %s\n", sig.Method)
			continue
		}

		if err := verifier.Verify(key, sig.Signature, msg); err != nil {
			log.Printf("continuing b/c signature was invalid\n")
			continue
		}
		valid[sig.KeyID] = struct{}{}

	}
	if len(valid) < roleData.Threshold {
		return ErrRoleThreshold
	}

	return nil
}

func Unmarshal(b []byte, v interface{}, role string, minVersion int, db *keys.DB) error {
	s := &data.Signed{}
	if err := json.Unmarshal(b, s); err != nil {
		return err
	}
	if err := Verify(s, role, minVersion, db); err != nil {
		return err
	}
	return json.Unmarshal(s.Signed, v)
}

func UnmarshalTrusted(b []byte, v interface{}, role string, db *keys.DB) error {
	s := &data.Signed{}
	if err := json.Unmarshal(b, s); err != nil {
		return err
	}
	if err := VerifySignatures(s, role, db); err != nil {
		return err
	}
	return json.Unmarshal(s.Signed, v)
}
