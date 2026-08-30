package ui

// ToastVariant is the enum-like string identifying a toast's status colour.
// Backed by .toast / .toast-{variant} classes added to @layer components
// in app.css alongside the existing .badge/.btn families.
type ToastVariant string

const (
	ToastVariantInfo    ToastVariant = "info"
	ToastVariantSuccess ToastVariant = "success"
	ToastVariantWarn    ToastVariant = "warn"
	ToastVariantError   ToastVariant = "error"
)

// ToastProps is the typed input to the Toast component. Title is optional;
// when omitted the toast is a single body line.
type ToastProps struct {
	Variant ToastVariant
	Title   string
	Body    string
}

func toastClass(v ToastVariant) string {
	switch v {
	case ToastVariantSuccess:
		return "toast toast-success"
	case ToastVariantWarn:
		return "toast toast-warn"
	case ToastVariantError:
		return "toast toast-error"
	default:
		return "toast toast-info"
	}
}
