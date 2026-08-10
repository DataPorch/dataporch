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
	if !validID(d.ID) {
		return fmt.Errorf("%w: invalid database id", ErrInvalidDefinition)
	}
	if strings.TrimSpace(string(d.Kind)) == "" {
		return fmt.Errorf("%w: missing adapter kind", ErrInvalidDefinition)
	}

	for name := range d.Settings {
		if !validFieldName(name) {
			return fmt.Errorf("%w: invalid setting name", ErrInvalidDefinition)
		}
	}
	for name, ref := range d.SecretRefs {
		if !validFieldName(name) {
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

func validID(id ID) bool {
	if len(id) == 0 || len(id) > maxIDLength {
		return false
	}
	for _, character := range id {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validFieldName(name string) bool {
	if name == "" || !isLetter(rune(name[0])) {
		return false
	}
	for _, character := range name[1:] {
		if isLetter(character) || (character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func isLetter(character rune) bool {
	return (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z')
}

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
