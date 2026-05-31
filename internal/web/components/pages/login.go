package pages

// ProviderButton is one OAuth sign-in option rendered on the login page.
// StartURL points at /auth/<name>/start (with the ?next= target preserved).
type ProviderButton struct {
	Name     string
	Label    string
	StartURL string
}
