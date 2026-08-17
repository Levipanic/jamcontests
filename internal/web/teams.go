package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type teamMemberView struct {
	ID                int64
	Username          string
	Captain           bool
	ProductEditor     bool
	QuestionnaireDone bool
}

type teamDetailView struct {
	ID              int64
	PublicID        string
	JamID           int64
	PublicJamID     string
	Name            string
	Description     string
	AvatarPath      string
	CaptainID       int64
	CaptainName     string
	MemberCount     int
	Members         []teamMemberView
	Eligible        bool
	InviteLive      bool
	IsMember        bool
	IsCaptain       bool
	CanManage       bool
	CanLeave        bool
	ThemePhrase     string
	CanEditProduct  bool
	ProductID       int64
	PublicProductID string
	ProductStatus   string
}

type teamPageView struct {
	User        *User
	CSRFToken   string
	Error       string
	Team        teamDetailView
	JamID       int64
	PublicJamID string
	Rules       string
}

type teamInviteView struct {
	User         *User
	CSRFToken    string
	Error        string
	Token        string
	TeamID       int64
	TeamPublicID string
	TeamName     string
	JamTitle     string
}

type teamLockedRecord struct {
	ID                 int64
	JamID              int64
	CaptainID          int64
	AvatarPath         *string
	MaxSize            int
	SubmissionStartsAt time.Time
	EvaluationStartsAt time.Time
	VotingStartsAt     time.Time
	FinishesAt         time.Time
	Override           *string
}

func (a *App) registerTeamRoutes(router *gin.Engine) {
	router.GET("/jams/:id/teams/new", RequireAuth(), a.teamNewPage)
	router.POST("/jams/:id/teams/new", RequireAuth(), a.teamCreate)
	router.GET("/teams/:id", a.teamDetail)
	router.POST("/teams/:id/edit", RequireAuth(), a.teamEdit)
	router.POST("/teams/:id/invite", RequireAuth(), a.teamInviteIssue)
	router.POST("/teams/:id/invite/revoke", RequireAuth(), a.teamInviteRevoke)
	router.GET("/invites/:token", RequireAuth(), a.teamInvitePage)
	router.POST("/invites/:token", RequireAuth(), a.teamInviteJoin)
	router.POST("/teams/:id/leave", RequireAuth(), a.teamLeave)
	router.POST("/teams/:id/captain", RequireAuth(), a.teamCaptainTransfer)
	router.POST("/teams/:id/product-editor", RequireAuth(), a.teamProductEditorToggle)
}

func (a *App) teamNewPage(c *gin.Context) {
	jamID, ok := a.resolvePublicID(c, "id", "jams")
	if !ok {
		return
	}
	publicJamID, err := a.publicIDOf(c.Request.Context(), "jams", jamID)
	if err != nil {
		a.logger.Error("load jam public id for team creation", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	allowed, err := a.teamPublishedManageableJam(c.Request.Context(), jamID)
	if err != nil {
		a.logger.Error("load jam for team creation", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if !allowed {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	var rules string
	if err = a.pool.QueryRow(c.Request.Context(), `SELECT rules FROM jams WHERE id=$1`, jamID).Scan(&rules); err != nil {
		a.logger.Error("load jam rules for team form", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	a.teamRender(c, http.StatusOK, "team_new.html", teamPageView{JamID: jamID, PublicJamID: publicJamID, Rules: rules, Error: c.Query("error")})
}

func (a *App) teamCreate(c *gin.Context) {
	jamID, ok := a.resolvePublicID(c, "id", "jams")
	if !ok {
		return
	}
	publicJamID, err := a.publicIDOf(c.Request.Context(), "jams", jamID)
	if err != nil {
		a.logger.Error("load jam public id for team creation", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	defer teamRemoveMultipartFiles(c)
	name := strings.TrimSpace(c.PostForm("name"))
	description := strings.TrimSpace(c.PostForm("description"))
	if err := teamValidateProfile(name, description); err != nil {
		teamRedirectError(c, fmt.Sprintf("/jams/%s/teams/new", publicJamID), err.Error())
		return
	}

	avatarPath, avatarWritten, err := a.teamStoreAvatar(c)
	if err != nil {
		teamRedirectError(c, fmt.Sprintf("/jams/%s/teams/new", publicJamID), err.Error())
		return
	}
	if avatarWritten {
		defer func() {
			if avatarWritten {
				a.teamRemoveAvatar(avatarPath)
			}
		}()
	}

	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.logger.Error("begin team creation", "error", err)
		teamRedirectError(c, fmt.Sprintf("/jams/%s/teams/new", publicJamID), "Не удалось создать команду. Попробуйте позже.")
		return
	}
	defer tx.Rollback(ctx)

	manageable, err := teamManageableJamTx(ctx, tx, jamID)
	if err != nil {
		a.logger.Error("validate jam for team creation", "error", err)
		teamRedirectError(c, fmt.Sprintf("/jams/%s/teams/new", publicJamID), "Не удалось создать команду. Попробуйте позже.")
		return
	}
	if !manageable {
		teamRedirectError(c, fmt.Sprintf("/jams/%s/teams/new", publicJamID), "Создание команд на этом джеме закрыто.")
		return
	}

	var jamRules string
	if err = tx.QueryRow(ctx, `SELECT rules FROM jams WHERE id=$1`, jamID).Scan(&jamRules); err != nil {
		a.logger.Error("load jam rules for team creation", "error", err)
		teamRedirectError(c, fmt.Sprintf("/jams/%s/teams/new", publicJamID), "Не удалось создать команду. Попробуйте позже.")
		return
	}
	if strings.TrimSpace(jamRules) != "" && c.PostForm("rules_confirmed") != "yes" {
		teamRedirectError(c, fmt.Sprintf("/jams/%s/teams/new", publicJamID), "Подтвердите, что ознакомлены с правилами проведения джема.")
		return
	}

	user := CurrentUser(c)
	teamPublicID, err := newPublicID()
	if err != nil {
		a.logger.Error("generate team public id", "error", err)
		teamRedirectError(c, fmt.Sprintf("/jams/%s/teams/new", publicJamID), "Не удалось создать команду. Попробуйте позже.")
		return
	}
	var teamID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO teams (jam_id, name, description, avatar_path, captain_user_id, public_id)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, jamID, name, description, teamNullableAvatar(avatarPath), user.ID, teamPublicID).Scan(&teamID)
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO team_members (team_id, jam_id, user_id) VALUES ($1, $2, $3)`, teamID, jamID, user.ID)
	}
	if err != nil {
		if teamConstraint(err, "teams_name_per_jam_ci_unique") {
			teamRedirectError(c, fmt.Sprintf("/jams/%s/teams/new", publicJamID), "Команда с таким названием уже существует в этом джеме.")
			return
		}
		if teamConstraint(err, "team_members_jam_id_user_id_key") {
			teamRedirectError(c, fmt.Sprintf("/jams/%s/teams/new", publicJamID), "Вы уже состоите в команде этого джема.")
			return
		}
		a.logger.Error("create team", "error", err)
		teamRedirectError(c, fmt.Sprintf("/jams/%s/teams/new", publicJamID), "Не удалось создать команду. Попробуйте позже.")
		return
	}
	// A failed COMMIT can have an unknown outcome. Keep the random file as an
	// inaccessible orphan rather than risk deleting a file referenced by DB.
	avatarWritten = false
	if err := tx.Commit(ctx); err != nil {
		a.logger.Error("commit team creation", "error", err)
		teamRedirectError(c, fmt.Sprintf("/jams/%s/teams/new", publicJamID), "Не удалось создать команду. Попробуйте позже.")
		return
	}
	c.Redirect(http.StatusSeeOther, fmt.Sprintf("/teams/%s", teamPublicID))
}

func (a *App) teamDetail(c *gin.Context) {
	teamID, ok := a.resolvePublicID(c, "id", "teams")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	var view teamDetailView
	var avatarPath *string
	var schedule Schedule
	var override *string
	err := a.pool.QueryRow(ctx, `
		SELECT t.id, t.public_id, t.jam_id, j.public_id, t.name, t.description,
		       t.avatar_path, t.captain_user_id,
		       captain.username, count(tm.user_id), j.submission_starts_at,
		       j.evaluation_starts_at, j.voting_starts_at, j.finishes_at, j.status_override
		FROM teams t
		JOIN jams j ON j.id = t.jam_id AND j.visibility = 'published'
		JOIN users captain ON captain.id = t.captain_user_id
		JOIN team_members tm ON tm.team_id = t.id
		WHERE t.id = $1
		GROUP BY t.id, t.public_id, j.public_id, captain.username, j.submission_starts_at,
		         j.evaluation_starts_at, j.voting_starts_at, j.finishes_at, j.status_override`, teamID).Scan(
		&view.ID, &view.PublicID, &view.JamID, &view.PublicJamID, &view.Name,
		&view.Description, &avatarPath, &view.CaptainID,
		&view.CaptainName, &view.MemberCount, &schedule.SubmissionStartsAt,
		&schedule.EvaluationStartsAt, &schedule.VotingStartsAt, &schedule.FinishesAt, &override)
	if err == pgx.ErrNoRows {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.logger.Error("load team detail", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if avatarPath != nil {
		view.AvatarPath = *avatarPath
	}
	if override != nil {
		stage := Stage(*override)
		schedule.Override = &stage
	}
	stage := EffectiveStage(schedule, time.Now())
	user := CurrentUser(c)
	var viewerProductEditor bool
	if user != nil {
		err = a.pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM team_members WHERE team_id=$1 AND user_id=$2),
			       COALESCE((SELECT is_product_editor FROM team_members WHERE team_id=$1 AND user_id=$2), false)`, teamID, user.ID).Scan(&view.IsMember, &viewerProductEditor)
		if err != nil {
			a.logger.Error("load team viewer membership", "error", err)
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
	}
	view.IsCaptain = user != nil && user.ID == view.CaptainID && view.IsMember
	view.CanManage = view.IsCaptain && CanManageTeam(stage)
	view.CanLeave = view.IsMember && CanManageTeam(stage) && (!view.IsCaptain || view.MemberCount == 1)
	view.CanEditProduct = stage == StageSubmission && view.IsMember && (view.IsCaptain || viewerProductEditor)
	if view.IsMember {
		err = a.pool.QueryRow(ctx, `SELECT status FROM products WHERE team_id=$1 AND jam_id=$2`, teamID, view.JamID).Scan(&view.ProductStatus)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			a.logger.Error("load team product status", "error", err)
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
	}
	if canDiscloseProducts(stage) {
		err = a.pool.QueryRow(ctx, `SELECT id, public_id FROM products WHERE team_id=$1 AND jam_id=$2 AND `+productDisclosedClause("products", stage), teamID, view.JamID).Scan(&view.ProductID, &view.PublicProductID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			a.logger.Error("load disclosed team product", "error", err)
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
	}
	if canDiscloseTeamTheme(stage, view.IsMember) {
		var phrase string
		err = a.pool.QueryRow(ctx, `
			SELECT theme.phrase FROM team_theme_selections selection
			JOIN jam_themes theme ON theme.id=selection.theme_id
			WHERE selection.team_id=$1`, teamID).Scan(&phrase)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			a.logger.Error("load disclosed team theme", "error", err)
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		if err == nil {
			view.ThemePhrase = phrase
		}
	}

	if view.IsMember {
		err = a.pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM team_members member
				JOIN questionnaires q ON q.jam_id = member.jam_id
				JOIN questionnaire_responses qr ON qr.questionnaire_id = q.id
					AND qr.revision=q.current_revision
					AND qr.user_id = member.user_id AND qr.status = 'completed'
				WHERE member.team_id = $1
			) OR COALESCE((SELECT allowed FROM team_eligibility_overrides WHERE team_id = $1), false)`, teamID).Scan(&view.Eligible)
		if err != nil {
			a.logger.Error("load team eligibility", "error", err)
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
	}
	if view.IsCaptain {
		err = a.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM team_invites WHERE team_id = $1 AND revoked_at IS NULL)`, teamID).Scan(&view.InviteLive)
		if err != nil {
			a.logger.Error("load team invite state", "error", err)
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
	}

	rows, err := a.pool.Query(ctx, `
		SELECT u.id, u.username, tm.is_product_editor,
		       CASE WHEN $2 THEN EXISTS (
			   SELECT 1 FROM questionnaires q
			   JOIN questionnaire_responses qr ON qr.questionnaire_id = q.id
			   WHERE q.jam_id = tm.jam_id AND qr.revision=q.current_revision
			     AND qr.user_id = tm.user_id AND qr.status = 'completed'
		       ) ELSE false END
		FROM team_members tm JOIN users u ON u.id = tm.user_id
		WHERE tm.team_id = $1
		ORDER BY (tm.user_id = $3) DESC, lower(u.username), u.id`, teamID, view.IsMember, view.CaptainID)
	if err != nil {
		a.logger.Error("load team members", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var member teamMemberView
		if err := rows.Scan(&member.ID, &member.Username, &member.ProductEditor, &member.QuestionnaireDone); err != nil {
			a.logger.Error("scan team member", "error", err)
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		member.Captain = member.ID == view.CaptainID
		if !view.IsMember {
			member.ProductEditor = false
		}
		view.Members = append(view.Members, member)
	}
	if err := rows.Err(); err != nil {
		a.logger.Error("iterate team members", "error", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	_, currentStage, recheckErr := a.loadPublishedJamStage(ctx, view.JamID)
	if errors.Is(recheckErr, pgx.ErrNoRows) || recheckErr == nil && currentStage != stage {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if recheckErr != nil {
		a.logger.Error("recheck team disclosure", "error", recheckErr)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	a.teamRender(c, http.StatusOK, "team_detail.html", teamPageView{Team: view, Error: c.Query("error")})
}

func (a *App) teamEdit(c *gin.Context) {
	publicID := c.Param("id")
	teamID, ok := a.resolvePublicID(c, "id", "teams")
	if !ok {
		return
	}
	defer teamRemoveMultipartFiles(c)
	name := strings.TrimSpace(c.PostForm("name"))
	description := strings.TrimSpace(c.PostForm("description"))
	if err := teamValidateProfile(name, description); err != nil {
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), err.Error())
		return
	}
	avatarPath, avatarWritten, err := a.teamStoreAvatar(c)
	if err != nil {
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), err.Error())
		return
	}
	if avatarWritten {
		defer func() {
			if avatarWritten {
				a.teamRemoveAvatar(avatarPath)
			}
		}()
	}

	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.logger.Error("begin team edit", "error", err)
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Не удалось изменить команду. Попробуйте позже.")
		return
	}
	defer tx.Rollback(ctx)
	team, err := teamLock(ctx, tx, teamID)
	if err != nil {
		a.teamMutationLoadError(c, publicID, "изменить команду", err)
		return
	}
	if !teamManageable(team) || team.CaptainID != CurrentUser(c).ID {
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Изменение команды сейчас недоступно.")
		return
	}
	newAvatar := team.AvatarPath
	if avatarWritten {
		newAvatar = &avatarPath
	}
	_, err = tx.Exec(ctx, `UPDATE teams SET name = $1, description = $2, avatar_path = $3, updated_at = now() WHERE id = $4`, name, description, newAvatar, teamID)
	if err != nil {
		if teamConstraint(err, "teams_name_per_jam_ci_unique") {
			teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Команда с таким названием уже существует в этом джеме.")
			return
		}
		a.logger.Error("update team", "error", err)
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Не удалось изменить команду. Попробуйте позже.")
		return
	}
	// See teamCreate: preserving an unreferenced random file is safer when the
	// transaction outcome is unknown than breaking a committed avatar link.
	avatarWritten = false
	if err := tx.Commit(ctx); err != nil {
		a.logger.Error("commit team edit", "error", err)
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Не удалось изменить команду. Попробуйте позже.")
		return
	}
	if avatarPath != "" && team.AvatarPath != nil && *team.AvatarPath != avatarPath {
		a.teamRemoveAvatar(*team.AvatarPath)
	}
	c.Redirect(http.StatusSeeOther, fmt.Sprintf("/teams/%s", publicID))
}

func (a *App) teamInviteIssue(c *gin.Context) {
	publicID := c.Param("id")
	teamID, ok := a.resolvePublicID(c, "id", "teams")
	if !ok {
		return
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		a.logger.Error("generate team invitation")
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Не удалось выпустить приглашение. Попробуйте позже.")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256(raw)
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.logger.Error("begin invitation issue")
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Не удалось выпустить приглашение. Попробуйте позже.")
		return
	}
	defer tx.Rollback(ctx)
	team, err := teamLock(ctx, tx, teamID)
	if err != nil || !teamManageable(team) || team.CaptainID != CurrentUser(c).ID {
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Выпуск приглашения сейчас недоступен.")
		return
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO team_invites (team_id, token_hash, created_by)
		VALUES ($1, $2, $3)
		ON CONFLICT (team_id) DO UPDATE
		SET token_hash = EXCLUDED.token_hash, created_by = EXCLUDED.created_by,
		    created_at = now(), revoked_at = NULL`, teamID, hash[:], CurrentUser(c).ID)
	if err != nil {
		a.logger.Error("store team invitation")
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Не удалось выпустить приглашение. Попробуйте позже.")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		a.logger.Error("commit invitation issue")
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Не удалось выпустить приглашение. Попробуйте позже.")
		return
	}
	c.HTML(http.StatusOK, "team_invite_issued.html", teamInviteView{User: CurrentUser(c), CSRFToken: csrfToken(c), Token: token, TeamID: teamID, TeamPublicID: publicID})
}

func (a *App) teamInviteRevoke(c *gin.Context) {
	publicID := c.Param("id")
	teamID, ok := a.resolvePublicID(c, "id", "teams")
	if !ok {
		return
	}
	replacement := make([]byte, 32)
	if _, err := rand.Read(replacement); err != nil {
		a.logger.Error("generate invitation revocation value")
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Не удалось отозвать приглашение. Попробуйте позже.")
		return
	}
	replacementHash := sha256.Sum256(replacement)
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.logger.Error("begin invitation revoke")
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Не удалось отозвать приглашение. Попробуйте позже.")
		return
	}
	defer tx.Rollback(ctx)
	team, err := teamLock(ctx, tx, teamID)
	if err != nil || !teamManageable(team) || team.CaptainID != CurrentUser(c).ID {
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Отзыв приглашения сейчас недоступен.")
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE team_invites SET token_hash = $2, revoked_at = now() WHERE team_id = $1 AND revoked_at IS NULL`, teamID, replacementHash[:]); err != nil {
		a.logger.Error("revoke team invitation")
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Не удалось отозвать приглашение. Попробуйте позже.")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		a.logger.Error("commit invitation revoke")
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Не удалось отозвать приглашение. Попробуйте позже.")
		return
	}
	c.Redirect(http.StatusSeeOther, fmt.Sprintf("/teams/%s", publicID))
}

func (a *App) teamInvitePage(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	token := c.Param("token")
	hash := teamInviteTokenHash(token)
	if hash == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	var view teamInviteView
	var schedule Schedule
	var override *string
	err := a.pool.QueryRow(c.Request.Context(), `
		SELECT t.id, t.public_id, t.name, j.title, j.submission_starts_at, j.evaluation_starts_at,
		       j.voting_starts_at, j.finishes_at, j.status_override
		FROM team_invites i
		JOIN teams t ON t.id = i.team_id
		JOIN jams j ON j.id = t.jam_id AND j.visibility = 'published'
		WHERE i.token_hash = $1 AND i.revoked_at IS NULL`, hash).Scan(
		&view.TeamID, &view.TeamPublicID, &view.TeamName, &view.JamTitle, &schedule.SubmissionStartsAt,
		&schedule.EvaluationStartsAt, &schedule.VotingStartsAt, &schedule.FinishesAt, &override)
	if err == pgx.ErrNoRows {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.logger.Error("load team invitation")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if override != nil {
		stage := Stage(*override)
		schedule.Override = &stage
	}
	if !CanManageTeam(EffectiveStage(schedule, time.Now())) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	view.Token = token
	view.Error = c.Query("error")
	a.teamRenderInvite(c, http.StatusOK, view)
}

func (a *App) teamInviteJoin(c *gin.Context) {
	token := c.Param("token")
	hash := teamInviteTokenHash(token)
	if hash == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.logger.Error("begin invitation join")
		teamRedirectError(c, "/invites/"+url.PathEscape(token), "Не удалось вступить в команду. Попробуйте позже.")
		return
	}
	defer tx.Rollback(ctx)
	var invitedTeamID int64
	err = tx.QueryRow(ctx, `
		SELECT t.id
		FROM team_invites i
		JOIN teams t ON t.id = i.team_id
		JOIN jams j ON j.id = t.jam_id AND j.visibility = 'published'
		WHERE i.token_hash = $1 AND i.revoked_at IS NULL`, hash).Scan(&invitedTeamID)
	if err == pgx.ErrNoRows {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.logger.Error("lock team invitation")
		teamRedirectError(c, "/invites/"+url.PathEscape(token), "Не удалось вступить в команду. Попробуйте позже.")
		return
	}
	team, err := teamLock(ctx, tx, invitedTeamID)
	if err != nil {
		a.logger.Error("lock invited team")
		teamRedirectError(c, "/invites/"+url.PathEscape(token), "Не удалось вступить в команду. Попробуйте позже.")
		return
	}
	var inviteCurrent bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM team_invites
			WHERE team_id = $1 AND token_hash = $2 AND revoked_at IS NULL
		)`, team.ID, hash).Scan(&inviteCurrent); err != nil || !inviteCurrent {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !teamManageable(team) {
		teamRedirectError(c, "/invites/"+url.PathEscape(token), "Вступление в команды этого джема закрыто.")
		return
	}
	userID := CurrentUser(c).ID
	var existingTeamID int64
	err = tx.QueryRow(ctx, `SELECT team_id FROM team_members WHERE jam_id = $1 AND user_id = $2`, team.JamID, userID).Scan(&existingTeamID)
	if err == nil {
		message := "Вы уже состоите в команде этого джема."
		if existingTeamID == team.ID {
			teamPublicID, publicErr := a.publicIDOf(ctx, "teams", team.ID)
			if publicErr != nil {
				a.logger.Error("load joined team public id", "error", publicErr)
				teamRedirectError(c, "/invites/"+url.PathEscape(token), "Не удалось вступить в команду. Попробуйте позже.")
				return
			}
			c.Redirect(http.StatusSeeOther, fmt.Sprintf("/teams/%s", teamPublicID))
			return
		}
		teamRedirectError(c, "/invites/"+url.PathEscape(token), message)
		return
	}
	if err != pgx.ErrNoRows {
		a.logger.Error("check invitation membership")
		teamRedirectError(c, "/invites/"+url.PathEscape(token), "Не удалось вступить в команду. Попробуйте позже.")
		return
	}
	var memberCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM team_members WHERE team_id = $1`, team.ID).Scan(&memberCount); err != nil {
		a.logger.Error("count invitation team members")
		teamRedirectError(c, "/invites/"+url.PathEscape(token), "Не удалось вступить в команду. Попробуйте позже.")
		return
	}
	if memberCount >= team.MaxSize {
		teamRedirectError(c, "/invites/"+url.PathEscape(token), "В команде больше нет свободных мест.")
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO team_members (team_id, jam_id, user_id) VALUES ($1, $2, $3)`, team.ID, team.JamID, userID)
	if err != nil {
		if teamConstraint(err, "team_members_jam_id_user_id_key") {
			teamRedirectError(c, "/invites/"+url.PathEscape(token), "Вы уже состоите в команде этого джема.")
			return
		}
		a.logger.Error("join team by invitation")
		teamRedirectError(c, "/invites/"+url.PathEscape(token), "Не удалось вступить в команду. Попробуйте позже.")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		a.logger.Error("commit invitation join")
		teamRedirectError(c, "/invites/"+url.PathEscape(token), "Не удалось вступить в команду. Попробуйте позже.")
		return
	}
	teamPublicID, err := a.publicIDOf(ctx, "teams", team.ID)
	if err != nil {
		a.logger.Error("load joined team public id", "error", err)
		teamRedirectError(c, "/invites/"+url.PathEscape(token), "Не удалось вступить в команду. Попробуйте позже.")
		return
	}
	c.Redirect(http.StatusSeeOther, fmt.Sprintf("/teams/%s", teamPublicID))
}

func (a *App) teamLeave(c *gin.Context) {
	publicID := c.Param("id")
	teamID, ok := a.resolvePublicID(c, "id", "teams")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.logger.Error("begin team leave", "error", err)
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Не удалось выйти из команды. Попробуйте позже.")
		return
	}
	defer tx.Rollback(ctx)
	team, err := teamLock(ctx, tx, teamID)
	if err != nil {
		a.teamMutationLoadError(c, publicID, "выйти из команды", err)
		return
	}
	userID := CurrentUser(c).ID
	if !teamManageable(team) {
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Изменение состава команды уже закрыто.")
		return
	}
	if team.CaptainID == userID {
		var members int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM team_members WHERE team_id=$1`, teamID).Scan(&members); err != nil {
			a.logger.Error("count team members on leave", "error", err)
			teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Не удалось выйти из команды. Попробуйте позже.")
			return
		}
		if members > 1 {
			teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Сначала передайте капитанство другому участнику.")
			return
		}
		if err = a.deleteTeamTx(ctx, tx, teamID); err != nil {
			a.logger.Error("delete last-member team", "error", err)
			teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Не удалось удалить команду. Попробуйте позже.")
			return
		}
	} else {
		result, err := tx.Exec(ctx, `DELETE FROM team_members WHERE team_id = $1 AND user_id = $2`, teamID, userID)
		if err != nil || result.RowsAffected() != 1 {
			teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Вы не состоите в этой команде.")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		a.logger.Error("commit team leave", "error", err)
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Не удалось выйти из команды. Попробуйте позже.")
		return
	}
	c.Redirect(http.StatusSeeOther, "/")
}

func (a *App) deleteTeamTx(ctx context.Context, tx pgx.Tx, teamID int64) error {
	statements := []string{
		`DELETE FROM nomination_votes WHERE nomination_id IN (SELECT id FROM nominations WHERE author_team_id=$1) OR product_id IN (SELECT id FROM products WHERE team_id=$1)`,
		`DELETE FROM nominations WHERE author_team_id=$1`,
		`DELETE FROM product_bumps WHERE product_id IN (SELECT id FROM products WHERE team_id=$1)`,
		`DELETE FROM products WHERE team_id=$1`,
		`DELETE FROM team_theme_selections WHERE team_id=$1`,
		`DELETE FROM team_eligibility_overrides WHERE team_id=$1`,
		`DELETE FROM team_invites WHERE team_id=$1`,
		`DELETE FROM team_members WHERE team_id=$1`,
		`DELETE FROM teams WHERE id=$1`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement, teamID); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) teamCaptainTransfer(c *gin.Context) {
	publicID := c.Param("id")
	teamID, ok := a.resolvePublicID(c, "id", "teams")
	targetID, targetOK := teamPositiveID(c.PostForm("user_id"))
	if !ok || !targetOK {
		if ok {
			teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Выберите участника для передачи капитанства.")
		} else {
			c.AbortWithStatus(http.StatusNotFound)
		}
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.logger.Error("begin captain transfer", "error", err)
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Не удалось передать капитанство. Попробуйте позже.")
		return
	}
	defer tx.Rollback(ctx)
	team, err := teamLock(ctx, tx, teamID)
	if err != nil || !teamManageable(team) || team.CaptainID != CurrentUser(c).ID {
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Передача капитанства сейчас недоступна.")
		return
	}
	result, err := tx.Exec(ctx, `
		UPDATE teams SET captain_user_id = $1, updated_at = now()
		WHERE id = $2 AND EXISTS (SELECT 1 FROM team_members WHERE team_id = $2 AND user_id = $1)`, targetID, teamID)
	if err != nil || result.RowsAffected() != 1 {
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Капитанство можно передать только текущему участнику.")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		a.logger.Error("commit captain transfer", "error", err)
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Не удалось передать капитанство. Попробуйте позже.")
		return
	}
	c.Redirect(http.StatusSeeOther, fmt.Sprintf("/teams/%s", publicID))
}

func (a *App) teamProductEditorToggle(c *gin.Context) {
	publicID := c.Param("id")
	teamID, ok := a.resolvePublicID(c, "id", "teams")
	targetID, targetOK := teamPositiveID(c.PostForm("user_id"))
	if !ok || !targetOK {
		if ok {
			teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Выберите участника.")
		} else {
			c.AbortWithStatus(http.StatusNotFound)
		}
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.logger.Error("begin product editor toggle", "error", err)
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Не удалось изменить полномочия. Попробуйте позже.")
		return
	}
	defer tx.Rollback(ctx)
	team, err := teamLock(ctx, tx, teamID)
	if err != nil || !teamManageable(team) || team.CaptainID != CurrentUser(c).ID {
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Изменение полномочий сейчас недоступно.")
		return
	}
	result, err := tx.Exec(ctx, `
		UPDATE team_members SET is_product_editor = NOT is_product_editor
		WHERE team_id = $1 AND user_id = $2`, teamID, targetID)
	if err != nil || result.RowsAffected() != 1 {
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Полномочия можно изменить только текущему участнику.")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		a.logger.Error("commit product editor toggle", "error", err)
		teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Не удалось изменить полномочия. Попробуйте позже.")
		return
	}
	c.Redirect(http.StatusSeeOther, fmt.Sprintf("/teams/%s", publicID))
}

func (a *App) teamPublishedManageableJam(ctx context.Context, jamID int64) (bool, error) {
	var schedule Schedule
	var override *string
	err := a.pool.QueryRow(ctx, `
		SELECT submission_starts_at, evaluation_starts_at, voting_starts_at, finishes_at, status_override
		FROM jams WHERE id = $1 AND visibility = 'published'`, jamID).Scan(
		&schedule.SubmissionStartsAt, &schedule.EvaluationStartsAt, &schedule.VotingStartsAt, &schedule.FinishesAt, &override)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if override != nil {
		stage := Stage(*override)
		schedule.Override = &stage
	}
	return CanManageTeam(EffectiveStage(schedule, time.Now())), nil
}

func teamManageableJamTx(ctx context.Context, tx pgx.Tx, jamID int64) (bool, error) {
	var team teamLockedRecord
	err := tx.QueryRow(ctx, `
		SELECT id, max_team_size, submission_starts_at, evaluation_starts_at,
		       voting_starts_at, finishes_at, status_override
		FROM jams WHERE id = $1 AND visibility = 'published' FOR SHARE`, jamID).Scan(
		&team.JamID, &team.MaxSize, &team.SubmissionStartsAt, &team.EvaluationStartsAt,
		&team.VotingStartsAt, &team.FinishesAt, &team.Override)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return teamManageable(team), nil
}

func teamLock(ctx context.Context, tx pgx.Tx, teamID int64) (teamLockedRecord, error) {
	var team teamLockedRecord
	err := tx.QueryRow(ctx, `
		SELECT t.id, t.jam_id, t.captain_user_id, t.avatar_path, j.max_team_size,
		       j.submission_starts_at, j.evaluation_starts_at, j.voting_starts_at,
		       j.finishes_at, j.status_override
		FROM teams t JOIN jams j ON j.id = t.jam_id AND j.visibility = 'published'
		WHERE t.id = $1 FOR UPDATE OF t FOR SHARE OF j`, teamID).Scan(
		&team.ID, &team.JamID, &team.CaptainID, &team.AvatarPath, &team.MaxSize,
		&team.SubmissionStartsAt, &team.EvaluationStartsAt, &team.VotingStartsAt,
		&team.FinishesAt, &team.Override)
	return team, err
}

func teamManageable(team teamLockedRecord) bool {
	return CanManageTeam(teamEffectiveStage(team))
}

func teamEffectiveStage(team teamLockedRecord) Stage {
	schedule := Schedule{
		SubmissionStartsAt: team.SubmissionStartsAt,
		EvaluationStartsAt: team.EvaluationStartsAt,
		VotingStartsAt:     team.VotingStartsAt,
		FinishesAt:         team.FinishesAt,
	}
	if team.Override != nil {
		stage := Stage(*team.Override)
		schedule.Override = &stage
	}
	return EffectiveStage(schedule, time.Now())
}

func (a *App) teamStoreAvatar(c *gin.Context) (string, bool, error) {
	header, err := c.FormFile("avatar")
	if errors.Is(err, http.ErrMissingFile) {
		return "", false, nil
	}
	if err != nil {
		return "", false, errors.New("Не удалось прочитать аватар.")
	}
	if header.Size > a.config.MaxAvatarBytes {
		return "", false, errors.New("Аватар превышает допустимый размер.")
	}
	file, err := header.Open()
	if err != nil {
		return "", false, errors.New("Не удалось прочитать аватар.")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, a.config.MaxAvatarBytes+1))
	if err != nil {
		return "", false, errors.New("Не удалось прочитать аватар.")
	}
	if int64(len(data)) > a.config.MaxAvatarBytes {
		return "", false, errors.New("Аватар превышает допустимый размер.")
	}
	contentType := http.DetectContentType(data)
	if contentType != "image/jpeg" && contentType != "image/png" && contentType != "image/webp" {
		return "", false, errors.New("Допустимы только изображения JPEG, PNG или WebP.")
	}
	processed, extension, err := processAvatar(data)
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(a.config.AvatarDir, 0o750); err != nil {
		a.logger.Error("create avatar directory", "error", err)
		return "", false, errors.New("Не удалось сохранить аватар. Попробуйте позже.")
	}
	randomName := make([]byte, 24)
	if _, err := rand.Read(randomName); err != nil {
		a.logger.Error("generate avatar filename")
		return "", false, errors.New("Не удалось сохранить аватар. Попробуйте позже.")
	}
	name := hex.EncodeToString(randomName) + extension
	path := filepath.Join(a.config.AvatarDir, name)
	stored, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		a.logger.Error("create avatar file", "error", err)
		return "", false, errors.New("Не удалось сохранить аватар. Попробуйте позже.")
	}
	if _, err := stored.Write(processed); err != nil {
		stored.Close()
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			a.logger.Warn("remove incomplete avatar", "error", removeErr)
		}
		return "", false, errors.New("Не удалось сохранить аватар. Попробуйте позже.")
	}
	if err := stored.Close(); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			a.logger.Warn("remove unclosed avatar", "error", removeErr)
		}
		return "", false, errors.New("Не удалось сохранить аватар. Попробуйте позже.")
	}
	return name, true, nil
}

func (a *App) teamRemoveAvatar(name string) {
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return
	}
	if err := os.Remove(filepath.Join(a.config.AvatarDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.logger.Warn("remove replaced avatar", "error", err)
	}
}

func (a *App) teamMutationLoadError(c *gin.Context, publicID, action string, err error) {
	if err == pgx.ErrNoRows {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	a.logger.Error("load team for mutation", "error", err)
	teamRedirectError(c, fmt.Sprintf("/teams/%s", publicID), "Не удалось "+action+". Попробуйте позже.")
}

func (a *App) teamRender(c *gin.Context, status int, templateName string, data teamPageView) {
	data.User = CurrentUser(c)
	data.CSRFToken = csrfToken(c)
	c.HTML(status, templateName, data)
}

func (a *App) teamRenderInvite(c *gin.Context, status int, data teamInviteView) {
	data.User = CurrentUser(c)
	data.CSRFToken = csrfToken(c)
	c.HTML(status, "team_invite.html", data)
}

func teamPositiveID(raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil && id > 0
}

func teamValidateProfile(name, description string) error {
	if utf8.RuneCountInString(name) < 2 || utf8.RuneCountInString(name) > 60 || hasControl(name) {
		return errors.New("Название команды должно содержать от 2 до 60 символов без управляющих знаков.")
	}
	if utf8.RuneCountInString(description) > 1000 || teamHasUnsafeTextControl(description) {
		return errors.New("Описание должно содержать не более 1000 символов.")
	}
	return nil
}

func teamHasUnsafeTextControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
	}) >= 0
}

func teamInviteTokenHash(token string) []byte {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return nil
	}
	hash := sha256.Sum256(raw)
	return hash[:]
}

func teamConstraint(err error, name string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == name
}

func teamRedirectError(c *gin.Context, path, message string) {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	c.Redirect(http.StatusSeeOther, path+separator+"error="+url.QueryEscape(message))
}

func teamNullableAvatar(path string) any {
	if path == "" {
		return nil
	}
	return path
}

func teamRemoveMultipartFiles(c *gin.Context) {
	if c.Request.MultipartForm != nil {
		_ = c.Request.MultipartForm.RemoveAll()
	}
}
