package catalog

import "testing"

func TestResource_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		resource  Resource
		wantError bool
	}{
		{
			name: "valid resource",
			resource: Resource{
				URI:  "memory://customers",
				Name: "Customers",
				Kind: "table",
			},
		},
		{
			name:      "missing uri",
			resource:  Resource{Name: "Customers", Kind: "table"},
			wantError: true,
		},
		{
			name:      "missing name",
			resource:  Resource{URI: "memory://customers", Kind: "table"},
			wantError: true,
		},
		{
			name:      "missing kind",
			resource:  Resource{URI: "memory://customers", Name: "Customers"},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.resource.Validate()
			if (err != nil) != tt.wantError {
				t.Fatalf("Validate() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}
