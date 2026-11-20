package device

// Test access to the per-destination admin plumbing.
var (
	AdminRequestTo = (*Client).adminRequestTo
	PostTo         = (*Client).postTo

	ConfigSectionsWithoutEnum = sectionsWithoutEnum
)
