package sqlite

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/adamraziv/dataporch/internal/connection"
)

const (
	Kind       connection.Kind = "sqlite"
	secretPath                 = "path"
)

var ErrInvalidConnectionString = errors.New("sqlite: invalid connection string")

type Adapter struct{}

var _ connection.Adapter = (*Adapter)(nil)

func New() *Adapter {
	return &Adapter{}
}

func (*Adapter) Kind() connection.Kind {
	return Kind
}

func (*Adapter) ParseConnectionString(input []byte) (connection.ParsedConnection, error) {
	path, err := parseConnectionURI(input)
	if err != nil {
		return connection.ParsedConnection{}, err
	}

	return connection.ParsedConnection{
		Settings: map[string]string{},
		Secrets: map[string][]byte{
			secretPath: []byte(path),
		},
	}, nil
}

func parseConnectionURI(input []byte) (string, error) {
	if !bytes.HasPrefix(input, []byte("sqlite:///")) {
		return "", invalidConnectionString("unsupported scheme")
	}

	uri, err := url.Parse(string(input))
	if err != nil || !utf8.Valid(input) {
		return "", invalidConnectionString("malformed uri")
	}

	if uri.Scheme != string(Kind) || uri.Opaque != "" {
		return "", invalidConnectionString("malformed uri")
	}

	if uri.Host != "" || uri.User != nil {
		return "", invalidConnectionString("authority not allowed")
	}

	if uri.RawQuery != "" || uri.ForceQuery {
		return "", invalidConnectionString("query not allowed")
	}

	if bytes.Contains(input, []byte{'#'}) {
		return "", invalidConnectionString("fragment not allowed")
	}

	escapedPath := uri.EscapedPath()
	if escapedPath == "" || !strings.HasPrefix(escapedPath, "/") {
		return "", invalidConnectionString("absolute path required")
	}

	path, err := url.PathUnescape(escapedPath)
	if err != nil || path == "" || !utf8.ValidString(path) || strings.IndexByte(path, '\x00') >= 0 {
		return "", invalidConnectionString("invalid path")
	}

	if !filepath.IsAbs(path) {
		return "", invalidConnectionString("absolute path required")
	}

	cleaned := filepath.Clean(path)
	if cleaned == "." || cleaned == string(filepath.Separator) || !filepath.IsAbs(cleaned) {
		return "", invalidConnectionString("absolute path required")
	}

	return cleaned, nil
}

func invalidConnectionString(category string) error {
	return fmt.Errorf("%w: %s", ErrInvalidConnectionString, category)
}
