package ui

import "strconv"

// LabelProps is the typed input to Label. For matches the `for` attribute
// on <label>; Text is the visible label string.
type LabelProps struct {
	For  string
	Text string
}

// InputProps is the typed input to Input. Type defaults to "text" when
// empty so zero-value InputProps renders a plain text input.
type InputProps struct {
	Name        string
	Type        string
	Value       string
	Placeholder string
	ID          string
	Class       string
	Required    bool
	Disabled    bool
}

func inputType(t string) string {
	if t == "" {
		return "text"
	}
	return t
}

// TextareaProps is the typed input to Textarea. Rows == 0 omits the
// attribute entirely so the browser default applies.
type TextareaProps struct {
	Name        string
	Value       string
	Placeholder string
	ID          string
	Class       string
	Rows        int
	Required    bool
	Disabled    bool
}

// rowsAttr renders an integer row count as a string, returning "" when
// Rows is 0 so the templ `if` guard can omit the attribute.
func rowsAttr(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// SelectOption is one <option> inside a Select.
type SelectOption struct {
	Value    string
	Label    string
	Selected bool
}

// SelectProps is the typed input to Select.
type SelectProps struct {
	Name     string
	Options  []SelectOption
	ID       string
	Class    string
	Required bool
	Disabled bool
}
