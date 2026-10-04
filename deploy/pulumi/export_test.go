package auditpulumi

// SetLibraryVersion makes the guards read the library's release as v, and returns
// what puts it back. A test binary of this module has no release of its own.
func SetLibraryVersion(v string) func() {
	was := libraryVersion
	libraryVersion = func() string { return v }
	return func() { libraryVersion = was }
}
