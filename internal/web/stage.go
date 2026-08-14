package web

import "time"

type Stage string

const (
	StageUpcoming   Stage = "upcoming"
	StageSubmission Stage = "submission"
	StageEvaluation Stage = "evaluation"
	StageVoting     Stage = "voting"
	StageFinished   Stage = "finished"
)

type Schedule struct {
	SubmissionStartsAt time.Time
	EvaluationStartsAt time.Time
	VotingStartsAt     time.Time
	FinishesAt         time.Time
	Override           *Stage
}

func EffectiveStage(schedule Schedule, now time.Time) Stage {
	if schedule.Override != nil {
		return *schedule.Override
	}
	switch {
	case now.Before(schedule.SubmissionStartsAt):
		return StageUpcoming
	case now.Before(schedule.EvaluationStartsAt):
		return StageSubmission
	case now.Before(schedule.VotingStartsAt):
		return StageEvaluation
	case now.Before(schedule.FinishesAt):
		return StageVoting
	default:
		return StageFinished
	}
}

func NextBoundary(schedule Schedule, stage Stage) *time.Time {
	var value time.Time
	switch stage {
	case StageUpcoming:
		value = schedule.SubmissionStartsAt
	case StageSubmission:
		value = schedule.EvaluationStartsAt
	case StageEvaluation:
		value = schedule.VotingStartsAt
	case StageVoting:
		value = schedule.FinishesAt
	default:
		return nil
	}
	return &value
}

func CanManageTeam(stage Stage) bool {
	return stage == StageUpcoming || stage == StageSubmission
}
