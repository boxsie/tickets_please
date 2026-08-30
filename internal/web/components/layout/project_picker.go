package layout

// pickerLabel returns the display label for the project picker toggle. It
// matches the active project by CurrentSlug; falls back to "Pick a project"
// when no project is selected or the slug doesn't match a mounted project.
func pickerLabel(data PageData) string {
	for _, p := range data.Chrome.Projects {
		if p.Slug == data.CurrentSlug {
			return p.Name
		}
	}
	return "Pick a project"
}

// pickerItemClass returns the picker list item's class string, adding
// `is-active` when the row matches CurrentSlug. Kept in Go (not inline in
// .templ) so the conditional is one place if the styling grows.
func pickerItemClass(slug, currentSlug string) string {
	if slug == currentSlug {
		return "project-picker-item is-active"
	}
	return "project-picker-item"
}
