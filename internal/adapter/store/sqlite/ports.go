package sqlite

import "github.com/alejandroayalad/billycore/internal/app"

// The adapters satisfy the ports they were built for, checked at compile time
// rather than at wiring time in main. A signature that drifts is a build
// failure here instead of a nil interface three layers away.
var (
	_ app.EvidenceRepository = (*EvidenceRepository)(nil)
	_ app.EvidenceQueue      = (*EvidenceQueue)(nil)
	_ app.ClaimRepository    = (*ClaimRepository)(nil)
)

// The reconciliation stage's ports. EvidenceQueue satisfies both stages'
// queues: they are separate interfaces because they are consumed by separate
// use cases (D6), not because two objects move rows through one table.
var (
	_ app.ReconcileQueue        = (*EvidenceQueue)(nil)
	_ app.TransactionRepository = (*TransactionRepository)(nil)
)
