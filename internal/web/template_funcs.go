package web

import (
	"time"
)

// templateFuncs contains display helpers shared by the administrative
// templates. They never influence application logic.
func templateFuncs() map[string]any {
	return map[string]any{
		"rusStage":       rusStageLabel,
		"rusVisibility":  rusVisibilityLabel,
		"add":            addInt,
		"sub":            subInt,
		"rusInviteState": rusInviteStateLabel,
		"rusStatus":      rusStatusLabel,
		"moscowDateTime": moscowDateTime,
	}
}

func rusStageLabel(stage Stage) string {
	switch stage {
	case StageUpcoming:
		return "Сбор"
	case StageSubmission:
		return "Сдача"
	case StageEvaluation:
		return "Оценка"
	case StageVoting:
		return "Голосование"
	case StageFinished:
		return "Финал"
	default:
		return string(stage)
	}
}

func rusVisibilityLabel(visibility string) string {
	switch visibility {
	case "draft":
		return "Черновик"
	case "published":
		return "Опубликован"
	default:
		return visibility
	}
}

func rusInviteStateLabel(state string) string {
	switch state {
	case "none":
		return "нет приглашения"
	case "active":
		return "активно"
	case "revoked":
		return "отозвано"
	default:
		return state
	}
}

func rusStatusLabel(status string) string {
	switch status {
	case "draft":
		return "Черновик"
	case "final":
		return "Сдан"
	case "completed":
		return "Завершено"
	case "not_started":
		return "Не начато"
	default:
		return status
	}
}

func addInt(left, right int) int {
	return left + right
}

func subInt(left, right int) int {
	return left - right
}

func moscowDateTime(value time.Time) string {
	location, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return value.UTC().Format("2006-01-02 15:04 UTC")
	}
	return value.In(location).Format("02.01.2006 15:04")
}
