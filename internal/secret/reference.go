package secret

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrInvalidReference = errors.New("secret: invalid reference")

type Reference string

func Parse(raw string) (Reference, error) {
	scheme, locator, found := strings.Cut(raw, "://")
	isValid := found && isValidScheme(scheme) && isValidLocator(locator)
	if !isValid {
		return "", ErrInvalidReference
	}

	return Reference(raw), nil
}

func NewLocal(identifier string) (Reference, error) {
	if strings.ContainsRune(identifier, '/') {
		return "", fmt.Errorf("%w: invalid local identifier", ErrInvalidReference)
	}

	return Parse("local://" + identifier)
}

func (r Reference) String() string {
	return string(r)
}

func (r Reference) Parts() (string, string, error) {
	parsed, err := Parse(string(r))
	if err != nil {
		return "", "", err
	}

	scheme, locator, _ := strings.Cut(parsed.String(), "://")
	return scheme, locator, nil
}

func isValidScheme(scheme string) bool {
	if len(scheme) == 0 {
		return false
	}
	if scheme[0] < 'a' || scheme[0] > 'z' {
		return false
	}

	for _, character := range scheme[1:] {
		if character >= 'a' && character <= 'z' {
			continue
		}
		if character >= '0' && character <= '9' {
			continue
		}
		if character == '-' {
			continue
		}
		return false
	}

	return true
}

func isValidLocator(locator string) bool {
	if locator == "" || !utf8.ValidString(locator) {
		return false
	}

	for _, character := range locator {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return false
		}
	}

	return true
}
