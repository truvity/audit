package catalogue

// Category is the kind of actor a treatment applies to. An actor kind declares
// its category in the catalogue; the treatment follows the category, never a
// field name.
type Category string

// Internal, External and Machine are the actor categories a preset sets a
// treatment for.
const (
	Internal Category = "internal" // staff, operators
	External Category = "external" // end users, a customer's people
	Machine  Category = "machine"  // services, API keys, the system
)

// Class is the field class an extension property declares with x-audit-class.
type Class string

// Shared, Audit, Metering, History and Evidence are the field classes an
// extension property declares and a preset keeps.
const (
	Shared   Class = "shared"
	Audit    Class = "audit"
	Metering Class = "metering"
	History  Class = "history"
	Evidence Class = "evidence"
)
