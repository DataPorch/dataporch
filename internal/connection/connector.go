package connection

import (
	"errors"
	"fmt"
	"reflect"
)

var (
	ErrAdapterNotFound = errors.New("connection: adapter not found")

	errAdapterRequired      = errors.New("connection: adapter required")
	errAdapterKindRequired  = errors.New("connection: adapter kind required")
	errDuplicateAdapterKind = errors.New("connection: duplicate adapter kind")
)

type Kind string

type Adapter interface {
	Kind() Kind
	ParseConnectionString([]byte) (ParsedConnection, error)
}

type Connector struct {
	adapters map[Kind]Adapter
}

func NewConnector(adapters ...Adapter) (*Connector, error) {
	index := make(map[Kind]Adapter, len(adapters))
	for _, adapter := range adapters {
		if isNilAdapter(adapter) {
			return nil, errAdapterRequired
		}

		kind := adapter.Kind()
		if kind == "" {
			return nil, errAdapterKindRequired
		}

		if _, exists := index[kind]; exists {
			return nil, fmt.Errorf("%w: %q", errDuplicateAdapterKind, kind)
		}

		index[kind] = adapter
	}

	return &Connector{adapters: index}, nil
}

func (c *Connector) Resolve(kind Kind) (Adapter, error) {
	adapter, exists := c.adapters[kind]
	if !exists {
		return nil, fmt.Errorf("%w: %q", ErrAdapterNotFound, kind)
	}

	return adapter, nil
}

func isNilAdapter(adapter Adapter) bool {
	if adapter == nil {
		return true
	}

	// Reflection is required to reject typed nil values stored in the adapter interface.
	value := reflect.ValueOf(adapter)
	switch value.Kind() {
	case reflect.Chan,
		reflect.Func,
		reflect.Interface,
		reflect.Map,
		reflect.Pointer,
		reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
