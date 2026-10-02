// Package buildinfo says which build this process is. The release stamps
// Version with the tag; a build from a checkout is "dev", which is what an
// observer on a record then reports.
package buildinfo

// Version is the release this binary was built from. It is set by the
// release's linker flags, never by configuration: what a process is does not
// depend on how it was deployed.
var Version = "dev"
