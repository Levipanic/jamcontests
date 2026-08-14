package web

import (
	"testing"
	"time"
)

func TestEffectiveStageBoundaries(t *testing.T) {
	base := time.Date(2026, time.August, 1, 9, 0, 0, 0, time.UTC)
	s := Schedule{
		SubmissionStartsAt: base.Add(time.Hour),
		EvaluationStartsAt: base.Add(2 * time.Hour),
		VotingStartsAt:     base.Add(3 * time.Hour),
		FinishesAt:         base.Add(4 * time.Hour),
	}
	tests := []struct {
		name string
		now  time.Time
		want Stage
	}{
		{"before submission", base, StageUpcoming},
		{"submission boundary", s.SubmissionStartsAt, StageSubmission},
		{"evaluation boundary", s.EvaluationStartsAt, StageEvaluation},
		{"voting boundary", s.VotingStartsAt, StageVoting},
		{"finished boundary", s.FinishesAt, StageFinished},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EffectiveStage(s, tt.now); got != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestEffectiveStageOverride(t *testing.T) {
	now := time.Now()
	override := StageVoting
	s := Schedule{
		SubmissionStartsAt: now.Add(time.Hour),
		EvaluationStartsAt: now.Add(2 * time.Hour),
		VotingStartsAt:     now.Add(3 * time.Hour),
		FinishesAt:         now.Add(4 * time.Hour),
		Override:           &override,
	}
	if got := EffectiveStage(s, now); got != StageVoting {
		t.Fatalf("got %s, want voting", got)
	}
}

func TestStageAtLeast(t *testing.T) {
	if StageAtLeast(StageUpcoming, StageSubmission) {
		t.Fatal("upcoming must be before submission")
	}
	if !StageAtLeast(StageSubmission, StageSubmission) || !StageAtLeast(StageFinished, StageSubmission) {
		t.Fatal("submission and later stages must satisfy comparison")
	}
}
