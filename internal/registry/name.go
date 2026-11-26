package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/soroworks/sorovault/internal/stellar"
	"github.com/soroworks/sorovault/internal/store"
)

// MaxNameLength is the longest display name, in characters, an operator
// may give a contract. It matches the limit on a publisher claim's name,
// so a name that verification would accept is one an operator can set too.
const MaxNameLength = maxClaimNameLength

// ErrInvalidName reports a contract name that is too long or contains
// control characters.
var ErrInvalidName = errors.New("registry: invalid contract name")

// NormalizeName trims a contract's display name and checks it.
//
// An empty result is valid: it means "no name given", and stores keep
// whatever name the contract already had.
func NormalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case utf8.RuneCountInString(name) > MaxNameLength:
		return "", fmt.Errorf("%w: name is longer than %d characters", ErrInvalidName, MaxNameLength)
	case strings.ContainsFunc(name, unicode.IsControl):
		return "", fmt.Errorf("%w: name contains control characters", ErrInvalidName)
	case !utf8.ValidString(name):
		return "", fmt.Errorf("%w: name is not valid UTF-8", ErrInvalidName)
	}
	return name, nil
}

// Rename sets a registered contract's display name.
//
// The name is normalised first, and must not be empty: renaming to nothing
// is not a way to clear a name, since every refresh would treat an empty
// name as "keep the existing one" anyway.
func (r *Registry) Rename(ctx context.Context, contractID, name string) (*store.Contract, error) {
	if err := stellar.ValidateContractID(contractID); err != nil {
		return nil, err
	}
	name, err := NormalizeName(name)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, fmt.Errorf("%w: name must not be empty", ErrInvalidName)
	}

	network, err := r.Network(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.store.SetName(ctx, network, contractID, name); err != nil {
		return nil, err
	}
	return r.store.GetContract(ctx, network, contractID)
}
