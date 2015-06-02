package data

import (
	"encoding/json"

	cjson "github.com/tent/canonical-json-go"
)

type SignedRoot struct {
	Signatures []Signature
	Signed     Root
}

type Root struct {
	Type               string             `json:"_type"`
	Version            int                `json:"version"`
	Expires            string             `json:"expires"`
	Keys               map[string]*TUFKey `json:"keys"`
	Roles              map[string]*Role   `json:"roles"`
	ConsistentSnapshot bool               `json:"consistent_snapshot"`
}

func (r SignedRoot) ToSigned() (*Signed, error) {
	s, err := cjson.Marshal(r.Signed)
	if err != nil {
		return nil, err
	}
	signed := json.RawMessage{}
	err = signed.UnmarshalJSON(s)
	if err != nil {
		return nil, err
	}
	sigs := make([]Signature, len(r.Signatures))
	copy(sigs, r.Signatures)
	return &Signed{
		Signatures: sigs,
		Signed:     signed,
	}, nil
}

func RootFromSigned(s *Signed) (*SignedRoot, error) {
	r := Root{}
	err := json.Unmarshal(s.Signed, &r)
	if err != nil {
		return nil, err
	}
	sigs := make([]Signature, len(s.Signatures))
	copy(sigs, s.Signatures)
	return &SignedRoot{
		Signatures: sigs,
		Signed:     r,
	}, nil
}
