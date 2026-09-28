package event

// RunEventMigrationCompleted records that an import from another agent's
// on-disk history finished. The report is the message the user was shown, so
// replay renders it verbatim instead of reconstructing it from counters that
// have since moved on.
const RunEventMigrationCompleted = "migration_completed"

// MigrationCompletedPayload is the durable form of a finished migration.
type MigrationCompletedPayload struct {
	Source string `json:"source"`
	Title  string `json:"title"`
	Report string `json:"report"`
}
