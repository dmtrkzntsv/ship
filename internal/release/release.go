// Package release describes a published release and its metadata files.
package release

import "time"

const (
	ReleaseFile = "release.json"
	SumsFile    = "SHA256SUMS"
)

// Release is the content of release.json.
type Release struct {
	Project string `json:"project"`
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	// Channel marks a moving channel's folder: it is the folder's own name
	// in <project>/<channel>/release.json and empty in a version folder.
	// latest and dev count as moving without it (older buckets).
	Channel     string    `json:"channel,omitempty"`
	PublishedAt time.Time `json:"published_at"`
}
