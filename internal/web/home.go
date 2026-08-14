package web

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

type HomeTeamView struct {
	ID          int64
	Name        string
	Description string
	AvatarPath  string
	MemberCount int
	MaxSize     int
	IsOwn       bool
	Eligible    bool
}

type ProfileView struct {
	Username            string
	Email               string
	TeamID              int64
	TeamName            string
	TeamRole            string
	QuestionnaireStatus string
	Eligible            bool
	IsCaptain           bool
}

func (a *App) populateHome(c *gin.Context, data *PageData) error {
	data.User = CurrentUser(c)
	jam, err := a.activeJam(c.Request.Context())
	if err != nil {
		return err
	}
	data.Jam = jam
	if jam != nil {
		teams, err := a.loadHomeTeams(c.Request.Context(), jam.ID, data.User, jam.MaxTeamSize)
		if err != nil {
			return err
		}
		data.Teams = teams
		if StageAtLeast(jam.Stage, StageSubmission) {
			themes, err := a.loadActiveThemes(c.Request.Context(), jam.ID)
			if err != nil {
				return err
			}
			data.Themes = themes
			data.ThemeConfigError = len(themes) == 0
		}
	}
	if data.User != nil {
		profile, err := a.loadProfile(c.Request.Context(), data.User, jam)
		if err != nil {
			return err
		}
		data.Profile = profile
		if jam != nil && StageAtLeast(jam.Stage, StageSubmission) && profile.TeamID != 0 {
			selected, err := a.loadTeamTheme(c.Request.Context(), profile.TeamID)
			if err != nil {
				return err
			}
			data.SelectedTheme = selected
		}
	}
	return nil
}

func (a *App) loadHomeTeams(ctx context.Context, jamID int64, user *User, maxSize int) ([]HomeTeamView, error) {
	var userID int64
	if user != nil {
		userID = user.ID
	}
	rows, err := a.pool.Query(ctx, `
		SELECT t.id, t.name, t.description, COALESCE(t.avatar_path, ''), count(tm.user_id),
		       COALESCE(bool_or(tm.user_id = $2), false),
		       EXISTS (
		           SELECT 1 FROM team_members eligible_member
		           JOIN questionnaires q ON q.jam_id = eligible_member.jam_id
		           JOIN questionnaire_responses response ON response.questionnaire_id = q.id
		                AND response.user_id = eligible_member.user_id AND response.status = 'completed'
		           WHERE eligible_member.team_id = t.id
		       ) OR COALESCE((SELECT allowed FROM team_eligibility_overrides WHERE team_id = t.id), false)
		FROM teams t
		JOIN team_members tm ON tm.team_id = t.id
		WHERE t.jam_id = $1
		GROUP BY t.id
		ORDER BY COALESCE(bool_or(tm.user_id = $2), false) DESC, t.created_at, t.id`, jamID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var teams []HomeTeamView
	for rows.Next() {
		var team HomeTeamView
		if err := rows.Scan(&team.ID, &team.Name, &team.Description, &team.AvatarPath, &team.MemberCount, &team.IsOwn, &team.Eligible); err != nil {
			return nil, err
		}
		team.MaxSize = maxSize
		if !team.IsOwn {
			team.Eligible = false
		}
		teams = append(teams, team)
	}
	return teams, rows.Err()
}

func (a *App) loadProfile(ctx context.Context, user *User, jam *JamView) (*ProfileView, error) {
	profile := &ProfileView{Username: user.Username}
	if user.Email != nil {
		profile.Email = *user.Email
	}
	if jam == nil {
		return profile, nil
	}
	var captainID int64
	err := a.pool.QueryRow(ctx, `
		SELECT t.id, t.name, t.captain_user_id, COALESCE(r.status, 'draft'),
		       EXISTS (
		           SELECT 1 FROM team_members eligible_member
		           JOIN questionnaires q2 ON q2.jam_id = eligible_member.jam_id
		           JOIN questionnaire_responses response ON response.questionnaire_id = q2.id
		                AND response.user_id = eligible_member.user_id AND response.status = 'completed'
		           WHERE eligible_member.team_id = t.id
		       ) OR COALESCE((SELECT allowed FROM team_eligibility_overrides WHERE team_id = t.id), false)
		FROM team_members tm
		JOIN teams t ON t.id = tm.team_id
		JOIN questionnaires q ON q.jam_id = tm.jam_id
		LEFT JOIN questionnaire_responses r ON r.questionnaire_id = q.id AND r.user_id = tm.user_id
		WHERE tm.jam_id = $1 AND tm.user_id = $2`, jam.ID, user.ID).Scan(
		&profile.TeamID, &profile.TeamName, &captainID, &profile.QuestionnaireStatus, &profile.Eligible)
	if errors.Is(err, pgx.ErrNoRows) {
		return profile, nil
	}
	if err != nil {
		return nil, err
	}
	profile.TeamRole = "участник"
	if captainID == user.ID {
		profile.TeamRole = "капитан"
		profile.IsCaptain = true
	}
	return profile, nil
}

func (a *App) loadActiveThemes(ctx context.Context, jamID int64) ([]ThemeView, error) {
	rows, err := a.pool.Query(ctx, `SELECT id, phrase FROM jam_themes WHERE jam_id=$1 AND withdrawn_at IS NULL ORDER BY created_at, id`, jamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var themes []ThemeView
	for rows.Next() {
		var theme ThemeView
		if err := rows.Scan(&theme.ID, &theme.Phrase); err != nil {
			return nil, err
		}
		themes = append(themes, theme)
	}
	return themes, rows.Err()
}

func (a *App) loadTeamTheme(ctx context.Context, teamID int64) (*ThemeView, error) {
	var theme ThemeView
	err := a.pool.QueryRow(ctx, `SELECT t.id, t.phrase FROM team_theme_selections s JOIN jam_themes t ON t.id=s.theme_id WHERE s.team_id=$1`, teamID).Scan(&theme.ID, &theme.Phrase)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &theme, nil
}

func (a *App) updateProfile(c *gin.Context) {
	user := CurrentUser(c)
	username := strings.TrimSpace(c.PostForm("username"))
	email, err := validateIdentity(username, c.PostForm("email"))
	if err != nil {
		profileRedirectError(c, err.Error())
		return
	}
	_, err = a.pool.Exec(c.Request.Context(), `UPDATE users SET username=$1, email=$2, updated_at=now() WHERE id=$3`, username, email, user.ID)
	if err != nil {
		if isUniqueViolation(err) {
			profileRedirectError(c, "Имя пользователя или email уже заняты.")
			return
		}
		a.logger.Error("update profile", "error", err)
		profileRedirectError(c, "Не удалось сохранить профиль. Попробуйте позже.")
		return
	}
	c.Redirect(http.StatusSeeOther, "/#profile")
}

func profileRedirectError(c *gin.Context, message string) {
	c.Redirect(http.StatusSeeOther, "/?profile_error="+urlQueryEscape(message)+"#profile")
}
