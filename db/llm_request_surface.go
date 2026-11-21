package db

// RequestSurface records normalized request structure, never content or headers.
// Hashes aid attribution; they do not prove cache hits or account identity.
type RequestSurface struct {
	Observed      bool   `json:"observed"`
	Model         string `json:"model,omitempty"`
	Format        string `json:"format,omitempty"`
	BuildVersion  string `json:"build_version,omitempty"`
	SystemSHA     string `json:"system_sha,omitempty"`
	ToolsSHA      string `json:"tools_sha,omitempty"`
	ParametersSHA string `json:"parameters_sha,omitempty"`
	EndpointSHA   string `json:"endpoint_sha,omitempty"`
	SystemBytes   int    `json:"system_bytes,omitempty"`
	ToolsBytes    int    `json:"tools_bytes,omitempty"`
	Attempts      int    `json:"attempts"`
	Changed       bool   `json:"changed_during_call,omitempty"`
}
