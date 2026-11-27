package identifier

import "testing"

func TestValidate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "empty identifier string", value: "", wantErr: true},
		{name: "identifier with leading dot", value: ".hidden_name", wantErr: true},
		{name: "identifier with leading hyphen", value: "-flag_name", wantErr: true},
		{name: "identifier with double dot traversal", value: "parent..child", wantErr: true},
		{name: "identifier with illegal symbol", value: "invalid@symbol", wantErr: true},
		{name: "identifier with path separator", value: "nested/token", wantErr: true},
		{name: "valid alphanumeric with dot dash underscore", value: "valid-name_01.sub", wantErr: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := Validate(tc.value, "ident_label")
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate(%q) error = %v, wantErr = %v", tc.value, err, tc.wantErr)
			}
		})
	}
}

func TestValidateList(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		values  []string
		wantErr bool
	}{
		{name: "empty list", values: nil, wantErr: false},
		{name: "all valid entries", values: []string{"app.example.com", "edge_01-node"}, wantErr: false},
		{name: "invalid entry in list", values: []string{"app.example.com", "../escape"}, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateList(tc.values, "list_label")
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateList(%v) error = %v, wantErr = %v", tc.values, err, tc.wantErr)
			}
		})
	}
}
