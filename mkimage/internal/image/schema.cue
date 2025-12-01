// image.cue — SST image manifest schema (v0.5)
// Validates the minimal metadata required for an SST image family.

// General image metadata.
image: {
    name:        string
    version?:    string
    description?: string
}

// Array of upstream sources providing artifacts.
// Each source corresponds to a subdirectory under the image directory.
// We do *not* validate the contents of these directories — filesystem is authoritative.
sources: [...{
    name:         string          // matches the subdirectory name, e.g. "cloud", "debug"
    upstream:     string          // URL or file path (original source)
    sha256?:      string          // SHA256 of the upstream blob
    format?:      string          // e.g. "qcow2", "iso", "raw", "oci"
    retrieved_at?: string         // optional timestamp
}]
