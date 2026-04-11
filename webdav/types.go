package webdav

import (
	"encoding/xml"
	"time"
)

// Multistatus is the top-level PROPFIND response.
type Multistatus struct {
	XMLName   xml.Name   `xml:"multistatus"`
	Responses []Response `xml:"response"`
}

// Response is a single resource entry in the multistatus.
type Response struct {
	Href     string     `xml:"href"`
	Propstat []Propstat `xml:"propstat"`
}

// Propstat groups properties with their HTTP status.
type Propstat struct {
	Props  Props  `xml:"prop"`
	Status string `xml:"status"`
}

// Props holds the WebDAV / OC namespace properties we care about.
type Props struct {
	Name            string       `xml:"name"`
	FileID          string       `xml:"fileid"`
	ETag            string       `xml:"getetag"`
	ContentLength   int64        `xml:"getcontentlength"`
	ContentType     string       `xml:"getcontenttype"`
	LastModifiedRaw string       `xml:"getlastmodified"`
	ResourceType    ResourceType `xml:"resourcetype"`
	Size            int64        `xml:"size"`
	Permissions     string       `xml:"permissions"`
}

// ResourceType indicates whether the resource is a collection (directory).
type ResourceType struct {
	Collection *struct{} `xml:"collection"`
}

// Resource is the parsed, normalised representation of a remote resource.
type Resource struct {
	// Href is the raw href from the server (URL-encoded path).
	Href string
	// Path is the decoded, relative path within the sync root.
	Path string
	// IsDir is true when the resource is a collection.
	IsDir bool
	// ETag is the server-assigned entity tag.
	ETag string
	// Size in bytes (0 for directories).
	Size int64
	// LastModified from the server.
	LastModified time.Time
	// FileID is the server-assigned immutable file identifier.
	FileID string
	// Permissions from oc:permissions.
	Permissions string
}
