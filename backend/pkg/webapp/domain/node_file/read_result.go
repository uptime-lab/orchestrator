package node_file

// ReadResult is what a track/refresh poll returns. It is built by the
// server from the command result — the raw result map is never returned, so a
// command id cannot be used to read some other command's decrypted output.
type ReadResult struct {
	Status    string `json:"status"` // running | completed | failed
	Detail    string `json:"detail,omitempty"`
	Path      string `json:"path,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Owner     string `json:"owner,omitempty"`
	Group     string `json:"group,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Version   int    `json:"version,omitempty"`
	Changed   bool   `json:"changed,omitempty"`
}

