package ui

// TableProps is the typed input to the Table component. Headers populate
// <thead>; the body is filled by children (caller writes the <tr>/<td>
// rows directly so per-cell rendering stays flexible).
type TableProps struct {
	Headers []string
	Class   string
}

func tableClass(p TableProps) string {
	cls := "data-table"
	if p.Class != "" {
		cls = cls + " " + p.Class
	}
	return cls
}
