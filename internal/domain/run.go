package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

type RunState string

const (
	RunPlanned      RunState = "planned"
	RunReserved     RunState = "reserved"
	RunFermenting   RunState = "fermenting"
	RunConditioning RunState = "conditioning"
	RunCompleted    RunState = "completed"
	RunAborted      RunState = "aborted"
)

type FermentationRun struct {
	ID               string               `json:"id"`
	Code             string               `json:"code"`
	FormulaID        string               `json:"formula_id"`
	VesselID         *string              `json:"vessel_id,omitempty"`
	PlannedVolumeL   float64              `json:"planned_volume_liters"`
	Notes            string               `json:"notes,omitempty"`
	State            RunState             `json:"state"`
	StartedAt        *time.Time           `json:"started_at,omitempty"`
	EndedAt          *time.Time           `json:"ended_at,omitempty"`
	Observations     []GravityObservation `json:"observations"`
	Metrics          *RunMetrics          `json:"metrics,omitempty"`
	CompletionDigest string               `json:"completion_digest,omitempty"`
	Version          int64                `json:"version"`
	CreatedAt        time.Time            `json:"created_at"`
	UpdatedAt        time.Time            `json:"updated_at"`
}

func (r FermentationRun) Clone() FermentationRun {
	cloned := r
	cloned.Observations = append([]GravityObservation(nil), r.Observations...)
	if r.VesselID != nil {
		value := *r.VesselID
		cloned.VesselID = &value
	}
	if r.StartedAt != nil {
		value := *r.StartedAt
		cloned.StartedAt = &value
	}
	if r.EndedAt != nil {
		value := *r.EndedAt
		cloned.EndedAt = &value
	}
	if r.Metrics != nil {
		value := *r.Metrics
		cloned.Metrics = &value
	}
	return cloned
}

func (r FermentationRun) Validate() error {
	if err := ValidateRunCode(r.Code, "code"); err != nil {
		return err
	}
	if r.PlannedVolumeL <= 0 {
		return FieldError(CodeInvalidInput, "planned_volume_liters",
			"must be greater than zero")
	}
	if len([]rune(r.Notes)) > 1000 {
		return FieldError(CodeInvalidInput, "notes",
			"must not exceed 1000 characters")
	}
	return nil
}

func (r *FermentationRun) ReserveVessel(vesselID string, now time.Time) error {
	if r.State != RunPlanned {
		return NewError(CodeRunState, "only a planned run can reserve a vessel")
	}
	value := vesselID
	r.VesselID = &value
	r.State = RunReserved
	r.UpdatedAt = now.UTC()
	r.Version++
	return nil
}

func (r *FermentationRun) StartFermentation(now time.Time) error {
	if r.State != RunReserved || r.VesselID == nil {
		return NewError(CodeRunState, "only a reserved run can start fermentation")
	}
	stamp := now.UTC()
	r.State = RunFermenting
	r.StartedAt = &stamp
	r.UpdatedAt = stamp
	r.Version++
	return nil
}

func (r *FermentationRun) AddObservation(observation GravityObservation) error {
	if r.State != RunFermenting || r.StartedAt == nil {
		return NewError(CodeRunState, "observations require a fermenting run")
	}
	if len(r.Observations) >= 500 {
		return NewError(CodeObservationConflict,
			"run cannot contain more than 500 observations")
	}
	var previous *GravityObservation
	if len(r.Observations) > 0 {
		value := r.Observations[len(r.Observations)-1]
		previous = &value
	}
	if err := ValidateObservation(*r.StartedAt, previous, observation, observation.ReceivedAt); err != nil {
		return err
	}
	expected := len(r.Observations) + 1
	if observation.Sequence != expected {
		return NewError(CodeObservationConflict,
			"observation sequence must be contiguous and begin at 1")
	}
	observation.RunID = r.ID
	r.Observations = append(r.Observations, observation)
	r.UpdatedAt = observation.ReceivedAt.UTC()
	r.Version++
	return nil
}

func (r *FermentationRun) BeginConditioning(now time.Time) error {
	if r.State != RunFermenting {
		return NewError(CodeRunState, "only a fermenting run can enter conditioning")
	}
	if len(r.Observations) < 2 {
		return NewError(CodeRunState,
			"conditioning requires at least two observations")
	}
	r.State = RunConditioning
	r.UpdatedAt = now.UTC()
	r.Version++
	return nil
}

func (r *FermentationRun) Complete(originalGravity float64, now time.Time) error {
	if r.State != RunConditioning {
		return NewError(CodeRunState, "only a conditioning run can be completed")
	}
	if len(r.Observations) < 3 {
		return NewError(CodeRunState,
			"completion requires at least three observations")
	}
	stamp := now.UTC()
	final := r.Observations[len(r.Observations)-1]
	metrics := CalculateMetrics(originalGravity, final.Gravity, len(r.Observations))
	r.Metrics = &metrics
	r.State = RunCompleted
	r.EndedAt = &stamp
	r.UpdatedAt = stamp
	r.Version++
	r.CompletionDigest = r.ExpectedCompletionDigest()
	return nil
}

// ExpectedCompletionDigest binds the immutable completion snapshot to the
// final run version and observation count.
func (r FermentationRun) ExpectedCompletionDigest() string {
	if r.Metrics == nil || r.EndedAt == nil {
		return ""
	}
	return CompletionDigest(
		r.ID,
		r.Version,
		*r.Metrics,
		*r.EndedAt,
		len(r.Observations),
	)
}

func CompletionDigest(
	runID string,
	version int64,
	metrics RunMetrics,
	endedAt time.Time,
	observationCount int,
) string {
	payload := fmt.Sprintf(
		"%s|%d|%.2f|%.2f|%.2f|%d|%s|%d",
		runID,
		version,
		metrics.FinalGravity,
		metrics.ApparentAttenuation,
		metrics.EstimatedAlcoholABV,
		metrics.ObservationCount,
		endedAt.UTC().Format(time.RFC3339Nano),
		observationCount,
	)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func (r FermentationRun) ValidateCompletionSnapshot() error {
	if r.State != RunCompleted {
		return nil
	}
	if r.Metrics == nil {
		return fmt.Errorf("completed run %s has no quality metrics", r.ID)
	}
	if r.EndedAt == nil {
		return fmt.Errorf("completed run %s has no completion time", r.ID)
	}
	if r.CompletionDigest == "" {
		return fmt.Errorf("completed run %s has no completion digest", r.ID)
	}
	if r.CompletionDigest != r.ExpectedCompletionDigest() {
		return fmt.Errorf(
			"completed run %s has a completion digest that does not match its final version",
			r.ID,
		)
	}
	return nil
}

// RehydrateCompletion restores the derived completion digest while loading a
// persisted snapshot. The persisted metrics are the quality result confirmed
// at completion, so they are never recomputed here; the digest is derived
// deterministically from the persisted run facts. Snapshots written before
// the digest existed, or with a digest that no longer matches those facts,
// are healed in memory without altering the confirmed metrics.
func (r *FermentationRun) RehydrateCompletion() error {
	if r.State != RunCompleted {
		return nil
	}
	if r.Metrics == nil {
		return fmt.Errorf("completed run %s has no quality metrics", r.ID)
	}
	if r.EndedAt == nil {
		return fmt.Errorf("completed run %s has no completion time", r.ID)
	}
	r.CompletionDigest = r.ExpectedCompletionDigest()
	return nil
}

func (r *FermentationRun) Abort(now time.Time) error {
	if r.State == RunCompleted || r.State == RunAborted {
		return NewError(CodeRunState, "completed or aborted runs cannot be aborted")
	}
	stamp := now.UTC()
	r.State = RunAborted
	r.EndedAt = &stamp
	r.UpdatedAt = stamp
	r.Version++
	return nil
}

func (r FermentationRun) HadStarted() bool {
	return r.StartedAt != nil
}
