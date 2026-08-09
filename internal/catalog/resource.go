package catalog

import "errors"

var (
	errURIRequired  = errors.New("catalog: resource uri is required")
	errNameRequired = errors.New("catalog: resource name is required")
	errKindRequired = errors.New("catalog: resource kind is required")
)

type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
}

func (r Resource) Validate() error {
	if r.URI == "" {
		return errURIRequired
	}

	if r.Name == "" {
		return errNameRequired
	}

	if r.Kind == "" {
		return errKindRequired
	}

	return nil
}
