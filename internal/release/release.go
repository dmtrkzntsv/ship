// Package release describes a published release and its metadata files.
package release

import "time"

const (
	ReleaseFile = "release.json"
	SumsFile    = "SHA256SUMS"
)

// Release is the content of release.json.
type Release struct {
	Project     string    `json:"project"`
	Version     string    `json:"version"`
	Commit      string    `json:"commit,omitempty"`
	PublishedAt time.Time `json:"published_at"`
}
