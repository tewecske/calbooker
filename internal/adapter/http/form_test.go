package httpadapter

import "testing"

func TestFormData(t *testing.T) {
	form := FormData{
		Submitted: true,
		Values:    map[string]string{"email": "person@example.com"},
		Errors: []FieldError{
			{Field: "email", Message: "Email is invalid"},
		},
	}
	if got := form.Value("email"); got != "person@example.com" {
		t.Errorf("Value(email) = %q, want submitted value", got)
	}
	if got := form.Error("email"); got != "Email is invalid" {
		t.Errorf("Error(email) = %q, want field error", got)
	}
	if !form.HasErrors() {
		t.Error("HasErrors() = false, want true")
	}

	beforeSubmit := FormData{Values: form.Values, Errors: form.Errors}
	if got := beforeSubmit.Value("email"); got != "" {
		t.Errorf("Value() before submit = %q, want empty", got)
	}
	if got := beforeSubmit.Error("email"); got != "" {
		t.Errorf("Error() before submit = %q, want empty", got)
	}
	if beforeSubmit.HasErrors() {
		t.Error("HasErrors() before submit = true, want false")
	}
}

func TestFormDataMissingFields(t *testing.T) {
	var form FormData
	if got := form.Value("missing"); got != "" {
		t.Errorf("Value(missing) = %q, want empty", got)
	}
	if got := form.Error("missing"); got != "" {
		t.Errorf("Error(missing) = %q, want empty", got)
	}
}
