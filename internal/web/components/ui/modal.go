package ui

import "strings"

// ModalProps is the typed input to the Modal component. ID is required —
// the dialog open/close JS scans for [data-dialog="<id>"] triggers and
// `dlg.showModal()` resolves the dialog by id.
type ModalProps struct {
	ID    string
	Title string
	Wide  bool // applies .modal-wide for wider content (forms, evidence panels)
}

func modalClass(p ModalProps) string {
	parts := []string{"modal"}
	if p.Wide {
		parts = append(parts, "modal-wide")
	}
	return strings.Join(parts, " ")
}
