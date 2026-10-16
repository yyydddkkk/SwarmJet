package domain

// Task stages. Task.Stage deliberately stays a string so stored tasks and API
// payloads are unchanged; these constants keep the workflow and the domain
// checks from spelling a stage as a bare literal.
const (
	StageQueued              = "queued"
	StageWaitingDependencies = "waiting_dependencies"
	StagePreparing           = "preparing"
	StageDeveloping          = "developing"
	StageVerifying           = "verifying"
	StagePublishing          = "publishing"
	StageReviewing           = "reviewing"
	StageWaitingApproval     = "waiting_approval"
	StageAcquiringMerge      = "acquiring_merge"
	StageMerging             = "merging"
	StagePartialMerge        = "partial_merge"
	StageWaitingManualMerge  = "waiting_manual_merge"
	StageManualMerging       = "manual_merging"
	StageFinalizing          = "finalizing"
	StageCompleted           = "completed"
	StageCancelled           = "cancelled"
	StageBlocked             = "blocked"
)
