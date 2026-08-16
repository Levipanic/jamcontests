package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"sort"
	"strconv"
	"strings"
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
		"auditDiff":      auditDiff,
	}
}

// auditDiff renders a readable before/after comparison of an audit entry.
// Values are JSON from immutable audit data; keys are application-controlled.
// Malformed or non-object payloads yield an empty result, letting templates
// fall back to the raw pre blocks.
func auditDiff(beforeRaw, afterRaw string) template.HTML {
	before := parseAuditValue(beforeRaw)
	after := parseAuditValue(afterRaw)
	if before == nil || after == nil {
		return ""
	}
	beforeMap, beforeIsMap := before.(map[string]any)
	afterMap, afterIsMap := after.(map[string]any)
	if !beforeIsMap || !afterIsMap {
		return ""
	}
	keys := make(map[string]bool)
	for key := range beforeMap {
		keys[key] = true
	}
	for key := range afterMap {
		keys[key] = true
	}
	sorted := make([]string, 0, len(keys))
	for key := range keys {
		sorted = append(sorted, key)
	}
	sort.Strings(sorted)
	var rows strings.Builder
	for _, key := range sorted {
		beforeValue := auditValueString(beforeMap[key])
		afterValue := auditValueString(afterMap[key])
		changed := auditValueEqual(beforeMap[key], afterMap[key])
		marker := ""
		rowClass := ""
		if !changed {
			marker = " ·"
			rowClass = ` class="audit-diff-changed"`
		}
		rows.WriteString("<tr" + rowClass + "><td>")
		rows.WriteString(template.HTMLEscapeString(key) + marker)
		rows.WriteString("</td><td>")
		rows.WriteString(template.HTMLEscapeString(beforeValue))
		rows.WriteString("</td><td>")
		rows.WriteString(template.HTMLEscapeString(afterValue))
		rows.WriteString("</td></tr>")
	}
	return template.HTML(`<table class="admin-audit-diff"><thead><tr><th>Поле</th><th>До</th><th>После</th></tr></thead><tbody>` + rows.String() + `</tbody></table>`)
}

type auditJSONValue = any

func parseAuditValue(raw string) auditJSONValue {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var value auditJSONValue
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil
	}
	return value
}

func auditValueString(value auditJSONValue) string {
	switch typed := value.(type) {
	case nil:
		return "—"
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case []auditJSONValue:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, auditValueString(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]auditJSONValue:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, key+":"+auditValueString(typed[key]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return strconv.Quote(strings.TrimSpace(strings.ReplaceAll(fmt.Sprint(value), "\n", " ")))
	}
}

func auditValueEqual(left, right auditJSONValue) bool {
	return auditValueString(left) == auditValueString(right)
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
