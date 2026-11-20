package server

// csvSafeCell prevents spreadsheet formula injection while preserving the
// displayed value inside CSV parsers. Prefixing a leading control character with
// a single quote is the standard Excel/Sheets mitigation.
func csvSafeCell(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + value
	}
	return value
}

func csvSafeRow(values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = csvSafeCell(value)
	}
	return out
}
