package connection

import (
	"errors"
	"fmt"
	"strings"

	"github.com/adamraziv/dataporch/internal/secret"
)

const maxIDLength = 128

var ErrInvalidDefinition = errors.New("connection: invalid definition")

type ID string

type Definition struct {
	ID         ID                          `json:"id"`
	Kind       Kind                        `json:"kind"`
	Settings   map[string]string           `json:"settings"`
	SecretRefs map[string]secret.Reference `json:"secretRefs"`
}

type ParsedConnection struct {
	Settings map[string]string
	Secrets  map[string][]byte
}

type ResolvedDefinition struct {
	ID       ID
	Kind     Kind
	Settings map[string]string
	Secrets  map[string][]byte
}

func (d Definition) Validate() error {
	if !isValidID(d.ID) {
		return fmt.Errorf("%w: invalid database id", ErrInvalidDefinition)
	}
	if strings.TrimSpace(string(d.Kind)) == "" {
		return fmt.Errorf("%w: missing adapter kind", ErrInvalidDefinition)
	}

	for name := range d.Settings {
		if !isValidFieldName(name) {
			return fmt.Errorf("%w: invalid setting name", ErrInvalidDefinition)
		}
	}
	for name, ref := range d.SecretRefs {
		if !isValidFieldName(name) {
			return fmt.Errorf("%w: invalid secret name", ErrInvalidDefinition)
		}
		if _, exists := d.Settings[name]; exists {
			return fmt.Errorf("%w: setting and secret names overlap", ErrInvalidDefinition)
		}
		if _, err := secret.Parse(ref.String()); err != nil {
			return fmt.Errorf("%w: invalid secret reference", ErrInvalidDefinition)
		}
	}

	return nil
}

func (d Definition) Clone() Definition {
	return Definition{
		ID:         d.ID,
		Kind:       d.Kind,
		Settings:   cloneStrings(d.Settings),
		SecretRefs: cloneReferences(d.SecretRefs),
	}
}

func (p ParsedConnection) Clone() ParsedConnection {
	return ParsedConnection{Settings: cloneStrings(p.Settings), Secrets: cloneBytes(p.Secrets)}
}

func (d ResolvedDefinition) Clone() ResolvedDefinition {
	return ResolvedDefinition{
		ID:       d.ID,
		Kind:     d.Kind,
		Settings: cloneStrings(d.Settings),
		Secrets:  cloneBytes(d.Secrets),
	}
}

func isValidID(id ID) bool {
	if len(id) == 0 || len(id) > maxIDLength {
		return false
	}
	for _, character := range id {
		if !isValidIDCharacter(character) {
			return false
		}
	}
	return true
}

func isValidFieldName(name string) bool {
	if name == "" || !isASCIILetter(rune(name[0])) {
		return false
	}
	for _, character := range name[1:] {
		if !isValidFieldCharacter(character) {
			return false
		}
	}
	return true
}

func isValidIDCharacter(character rune) bool {
	switch character {
	case '.', '_', '-':
		return true
	default:
		return isASCIILetter(character) || isASCIIDigit(character)
	}
}

func isValidFieldCharacter(character rune) bool {
	switch character {
	case '_', '-':
		return true
	default:
		return isASCIILetter(character) || isASCIIDigit(character)
	}
}

func isASCIILetter(character rune) bool {
	isLowercase := character >= 'a' && character <= 'z'
	isUppercase := character >= 'A' && character <= 'Z'
	return isLowercase || isUppercase
}

func isASCIIDigit(character rune) bool {
	return character >= '0' && character <= '9'
}

// Clone helpers preserve nil maps so clones retain the source representation.
func cloneStrings(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for name, value := range values {
		cloned[name] = value
	}
	return cloned
}

func cloneReferences(values map[string]secret.Reference) map[string]secret.Reference {
	if values == nil {
		return nil
	}
	cloned := make(map[string]secret.Reference, len(values))
	for name, value := range values {
		cloned[name] = value
	}
	return cloned
}

func cloneBytes(values map[string][]byte) map[string][]byte {
	if values == nil {
		return nil
	}
	cloned := make(map[string][]byte, len(values))
	for name, value := range values {
		cloned[name] = append([]byte(nil), value...)
	}
	return cloned
}
