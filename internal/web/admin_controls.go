package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

const adminRoleLock int64 = 0x41444d49

type adminControlPageData struct {
	User       *User
	CSRFToken  string
	Error      string
	Ok         string
	Users      []adminControlUser
	Teams      []adminControlTeam
	Search     string
	UserDetail *adminControlUserDetail
	Pager      *adminPager
	JamFilter  int64
	Jams       []adminJam
}

type adminControlUser struct {
	ID       int64
	Username string
	Email    *string
	Role     string
}

type adminControlUserMembership struct {
	JamID         int64
	JamTitle      string
	TeamID        int64
	TeamName      string
	Captain       bool
	ProductEditor bool
}

type adminControlUserDetail struct {
	adminControlUser
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Memberships []adminControlUserMembership
}

type adminControlMember struct {
	ID       int64
	Username string
	Captain  bool
}

type adminControlTeam struct {
	ID                  int64
	JamID               int64
	JamTitle            string
	Name                string
	Description         string
	AvatarPath          *string
	CaptainID           int64
	CaptainName         string
	MaxSize             int
	InviteState         string
	EligibilityOverride *bool
	OverrideMeta        string
	CompletedEligible   bool
	Members             []adminControlMember
}

type adminControlLockedTeam struct {
	ID          int64
	JamID       int64
	Name        string
	Description string
	AvatarPath  *string
	CaptainID   int64
	MaxSize     int
}

// registerAdminControlRoutes installs administrative controls outside the jam editor.
func (a *App) registerAdminControlRoutes(router *gin.Engine) {
	admin := router.Group("/admin", RequireAdmin())
	admin.GET("/users", a.adminControlUsersPage)
	admin.GET("/users/:id", a.adminControlUserDetailPage)
	admin.POST("/users/:id/role", a.adminControlUserRole)
	admin.GET("/teams", a.adminControlTeamsPage)
	admin.POST("/teams/:id/profile", a.adminControlTeamProfile)
	admin.POST("/teams/:id/avatar/remove", a.adminControlTeamAvatarRemove)
	admin.POST("/teams/:id/invite/revoke", a.adminControlTeamInviteRevoke)
	admin.POST("/teams/:id/members/add", a.adminControlTeamMemberAdd)
	admin.POST("/teams/:id/members/:userID/remove", a.adminControlTeamMemberRemove)
	admin.POST("/teams/:id/captain", a.adminControlTeamCaptain)
	admin.POST("/teams/:id/eligibility", a.adminControlTeamEligibility)
}

func (a *App) adminControlUsersPage(c *gin.Context) {
	search := strings.TrimSpace(c.Query("q"))
	if len(search) > 254 {
		a.adminControlRender(c, http.StatusBadRequest, "admin_users.html", adminControlPageData{Error: "Поисковый запрос слишком длинный.", Search: search})
		return
	}
	page, per := adminPageParam(c)
	var exactID int64
	if parsed, parseErr := strconv.ParseInt(search, 10, 64); parseErr == nil && parsed > 0 {
		exactID = parsed
	}
	var total int
	if err := a.pool.QueryRow(c.Request.Context(), `
		SELECT count(*) FROM users
		WHERE $1='' OR strpos(lower(username), lower($1))>0
		   OR strpos(lower(COALESCE(email, '')), lower($1))>0 OR id=$2`, search, exactID).Scan(&total); err != nil {
		a.adminControlRender(c, http.StatusInternalServerError, "admin_users.html", adminControlPageData{Error: "Не удалось загрузить пользователей."})
		return
	}
	rows, err := a.pool.Query(c.Request.Context(), `
		SELECT id, username, email, role FROM users
		WHERE $1='' OR strpos(lower(username), lower($1))>0
		   OR strpos(lower(COALESCE(email, '')), lower($1))>0 OR id=$2
		ORDER BY lower(username), id OFFSET $3 LIMIT $4`, search, exactID, (page-1)*per, per)
	if err != nil {
		a.adminControlRender(c, http.StatusInternalServerError, "admin_users.html", adminControlPageData{Error: "Не удалось загрузить пользователей."})
		return
	}
	defer rows.Close()
	var users []adminControlUser
	for rows.Next() {
		var user adminControlUser
		if err := rows.Scan(&user.ID, &user.Username, &user.Email, &user.Role); err != nil {
			a.logger.Error("scan admin users", "error", err)
			a.adminControlRender(c, http.StatusInternalServerError, "admin_users.html", adminControlPageData{Error: "Не удалось загрузить пользователей."})
			return
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		a.logger.Error("load admin users", "error", err)
		a.adminControlRender(c, http.StatusInternalServerError, "admin_users.html", adminControlPageData{Error: "Не удалось загрузить пользователей."})
		return
	}
	a.adminControlRender(c, http.StatusOK, "admin_users.html", adminControlPageData{
		Users: users, Error: c.Query("error"), Ok: c.Query("ok"), Search: search,
		Pager: buildAdminPager("/admin/users?q="+url.QueryEscape(search), page, per, total),
	})
}

func (a *App) adminControlUserDetailPage(c *gin.Context) {
	userID, ok := adminID(c, "id")
	if !ok {
		return
	}
	var detail adminControlUserDetail
	err := a.pool.QueryRow(c.Request.Context(), `SELECT id, username, email, role, created_at, updated_at FROM users WHERE id=$1`, userID).Scan(&detail.ID, &detail.Username, &detail.Email, &detail.Role, &detail.CreatedAt, &detail.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if err != nil {
		a.adminControlRender(c, http.StatusInternalServerError, "admin_user_detail.html", adminControlPageData{Error: "Не удалось загрузить пользователя."})
		return
	}
	rows, err := a.pool.Query(c.Request.Context(), `
		SELECT jam.id, jam.title, team.id, team.name, team.captain_user_id=member.user_id, member.is_product_editor
		FROM team_members member JOIN teams team ON team.id=member.team_id AND team.jam_id=member.jam_id
		JOIN jams jam ON jam.id=member.jam_id WHERE member.user_id=$1 ORDER BY jam.created_at DESC`, userID)
	if err != nil {
		a.adminControlRender(c, http.StatusInternalServerError, "admin_user_detail.html", adminControlPageData{Error: "Не удалось загрузить членство."})
		return
	}
	defer rows.Close()
	for rows.Next() {
		var membership adminControlUserMembership
		if err = rows.Scan(&membership.JamID, &membership.JamTitle, &membership.TeamID, &membership.TeamName, &membership.Captain, &membership.ProductEditor); err != nil {
			a.adminControlRender(c, http.StatusInternalServerError, "admin_user_detail.html", adminControlPageData{Error: "Не удалось загрузить членство."})
			return
		}
		detail.Memberships = append(detail.Memberships, membership)
	}
	a.adminControlRender(c, http.StatusOK, "admin_user_detail.html", adminControlPageData{UserDetail: &detail, Error: c.Query("error"), Ok: c.Query("ok")})
}

func (a *App) adminControlUserRole(c *gin.Context) {
	userID, ok := adminID(c, "id")
	if !ok {
		return
	}
	role := strings.TrimSpace(c.PostForm("role"))
	if role != "user" && role != "admin" {
		a.adminControlRedirect(c, "/admin/users", "Допустимы только роли user и admin.")
		return
	}
	if c.PostForm("confirm") != "change_role" {
		a.adminControlRedirect(c, "/admin/users", "Подтвердите изменение роли.")
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.adminControlFailure(c, "/admin/users", "begin user role update", err)
		return
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, adminRoleLock); err != nil {
		a.adminControlFailure(c, "/admin/users", "lock admin roles", err)
		return
	}
	var beforeRole string
	if err = tx.QueryRow(ctx, `SELECT role FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&beforeRole); err != nil {
		a.adminControlMutationLoadError(c, "/admin/users", "load user role", err)
		return
	}
	if beforeRole == role {
		a.adminControlRedirect(c, "/admin/users", "У пользователя уже выбрана эта роль.")
		return
	}
	if beforeRole == "admin" && role == "user" {
		var adminCount int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE role='admin'`).Scan(&adminCount); err != nil {
			a.adminControlFailure(c, "/admin/users", "count administrators", err)
			return
		}
		if adminCount <= 1 {
			a.adminControlRedirect(c, "/admin/users", "Нельзя понизить последнего администратора.")
			return
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET role=$1, updated_at=now() WHERE id=$2`, role, userID); err != nil {
		a.adminControlFailure(c, "/admin/users", "update user role", err)
		return
	}
	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID); err != nil {
		a.adminControlFailure(c, "/admin/users", "revoke sessions after role update", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.adminControlFailure(c, "/admin/users", "commit user role update", err)
		return
	}
	adminOkRedirect(c, "/admin/users", "Роль пользователя изменена, сессии отозваны.")
}

func (a *App) adminControlTeamsPage(c *gin.Context) {
	var jamFilter int64
	if raw := c.Query("jam"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 1 {
			a.adminControlRender(c, http.StatusBadRequest, "admin_teams.html", adminControlPageData{Error: "Некорректный фильтр джема."})
			return
		}
		jamFilter = parsed
	}
	teams, err := a.loadAdminControlTeams(c.Request.Context(), jamFilter)
	if err != nil {
		a.logger.Error("load admin teams", "error", err)
		a.adminControlRender(c, http.StatusInternalServerError, "admin_teams.html", adminControlPageData{Error: "Не удалось загрузить команды."})
		return
	}
	jams, err := a.loadAdminJams(c.Request.Context())
	if err != nil {
		a.logger.Error("load admin jam filter options", "error", err)
		a.adminControlRender(c, http.StatusInternalServerError, "admin_teams.html", adminControlPageData{Error: "Не удалось загрузить джемы."})
		return
	}
	a.adminControlRender(c, http.StatusOK, "admin_teams.html", adminControlPageData{
		Teams: teams, Error: c.Query("error"), Ok: c.Query("ok"), JamFilter: jamFilter, Jams: jams,
	})
}

func (a *App) adminControlTeamProfile(c *gin.Context) {
	teamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	name := strings.TrimSpace(c.PostForm("name"))
	description := strings.TrimSpace(c.PostForm("description"))
	if err := teamValidateProfile(name, description); err != nil {
		a.adminControlRedirect(c, "/admin/teams", err.Error())
		return
	}
	ctx, tx, _, ok := a.adminControlBeginTeam(c, teamID, "изменить профиль команды")
	if !ok {
		return
	}
	defer tx.Rollback(ctx)
	var err error
	if _, err = tx.Exec(ctx, `UPDATE teams SET name=$1, description=$2, updated_at=now() WHERE id=$3`, name, description, teamID); err != nil {
		if teamConstraint(err, "teams_name_per_jam_ci_unique") {
			a.adminControlRedirect(c, "/admin/teams", "Команда с таким названием уже существует в этом джеме.")
			return
		}
		a.adminControlFailure(c, "/admin/teams", "update team profile", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.adminControlFailure(c, "/admin/teams", "commit team profile", err)
		return
	}
	adminOkRedirect(c, "/admin/teams", "Профиль команды обновлён.")
}

func (a *App) adminControlTeamAvatarRemove(c *gin.Context) {
	teamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	ctx, tx, team, ok := a.adminControlBeginTeam(c, teamID, "удалить аватар команды")
	if !ok {
		return
	}
	defer tx.Rollback(ctx)
	if team.AvatarPath == nil {
		a.adminControlRedirect(c, "/admin/teams", "У команды нет аватара.")
		return
	}
	avatarPath := *team.AvatarPath
	var err error
	if _, err = tx.Exec(ctx, `UPDATE teams SET avatar_path=NULL, updated_at=now() WHERE id=$1`, teamID); err != nil {
		a.adminControlFailure(c, "/admin/teams", "remove team avatar record", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.adminControlFailure(c, "/admin/teams", "commit team avatar removal", err)
		return
	}
	a.teamRemoveAvatar(avatarPath)
	adminOkRedirect(c, "/admin/teams", "Аватар команды удалён.")
}

func (a *App) adminControlTeamInviteRevoke(c *gin.Context) {
	teamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	replacement := make([]byte, 32)
	if _, err := rand.Read(replacement); err != nil {
		a.adminControlFailure(c, "/admin/teams", "generate invite revocation", err)
		return
	}
	replacementHash := sha256.Sum256(replacement)
	ctx, tx, _, ok := a.adminControlBeginTeam(c, teamID, "отозвать приглашение")
	if !ok {
		return
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE team_invites SET token_hash=$2, revoked_at=now() WHERE team_id=$1 AND revoked_at IS NULL`, teamID, replacementHash[:])
	if err != nil {
		a.adminControlFailure(c, "/admin/teams", "revoke team invite", err)
		return
	}
	if result.RowsAffected() != 1 {
		a.adminControlRedirect(c, "/admin/teams", "У команды нет активного приглашения.")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.adminControlFailure(c, "/admin/teams", "commit team invite revocation", err)
		return
	}
	adminOkRedirect(c, "/admin/teams", "Приглашение отозвано.")
}

func (a *App) adminControlTeamMemberAdd(c *gin.Context) {
	teamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	username := strings.TrimSpace(c.PostForm("username"))
	if username == "" {
		a.adminControlRedirect(c, "/admin/teams", "Укажите имя пользователя.")
		return
	}
	ctx, tx, team, ok := a.adminControlBeginTeam(c, teamID, "добавить участника")
	if !ok {
		return
	}
	defer tx.Rollback(ctx)
	var userID int64
	var err error
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE lower(username)=lower($1)`, username).Scan(&userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			a.adminControlRedirect(c, "/admin/teams", "Пользователь не найден.")
			return
		}
		a.adminControlFailure(c, "/admin/teams", "load member to add", err)
		return
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(format('vote-membership:%s:%s', $1::bigint, $2::bigint), 0))`, team.JamID, userID); err != nil {
		a.adminControlFailure(c, "/admin/teams", "lock member votes", err)
		return
	}
	var hasSelfVote bool
	if err = tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM nomination_votes vote
			JOIN products product ON product.id=vote.product_id AND product.jam_id=vote.jam_id
			WHERE vote.user_id=$1 AND vote.jam_id=$2 AND product.team_id=$3 AND vote.invalidated_at IS NULL
		)`, userID, team.JamID, teamID).Scan(&hasSelfVote); err != nil {
		a.adminControlFailure(c, "/admin/teams", "check member votes", err)
		return
	}
	if hasSelfVote {
		a.adminControlRedirect(c, "/admin/teams", "Сначала инвалидируйте активные голоса пользователя за продукт этой команды.")
		return
	}
	var memberCount int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM team_members WHERE team_id=$1`, teamID).Scan(&memberCount); err != nil {
		a.adminControlFailure(c, "/admin/teams", "count team members", err)
		return
	}
	if memberCount >= team.MaxSize {
		a.adminControlRedirect(c, "/admin/teams", "В команде нет свободных мест.")
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO team_members (team_id, jam_id, user_id) VALUES ($1, $2, $3)`, teamID, team.JamID, userID); err != nil {
		if teamConstraint(err, "team_members_jam_id_user_id_key") || teamConstraint(err, "team_members_pkey") {
			a.adminControlRedirect(c, "/admin/teams", "Пользователь уже состоит в команде этого джема.")
			return
		}
		a.adminControlFailure(c, "/admin/teams", "add team member", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.adminControlFailure(c, "/admin/teams", "commit team member addition", err)
		return
	}
	adminOkRedirect(c, "/admin/teams", "Участник добавлен в команду.")
}

func (a *App) adminControlTeamMemberRemove(c *gin.Context) {
	teamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	userID, ok := adminID(c, "userID")
	if !ok {
		return
	}
	ctx, tx, team, ok := a.adminControlBeginTeam(c, teamID, "удалить участника")
	if !ok {
		return
	}
	defer tx.Rollback(ctx)
	var err error
	if team.CaptainID == userID {
		a.adminControlRedirect(c, "/admin/teams", "Нельзя удалить капитана. Сначала назначьте другого капитана.")
		return
	}
	if err = tx.QueryRow(ctx, `SELECT 1 FROM team_members WHERE team_id=$1 AND user_id=$2`, teamID, userID).Scan(new(int)); err != nil {
		a.adminControlMutationLoadError(c, "/admin/teams", "load team member", err)
		return
	}
	if _, err = tx.Exec(ctx, `DELETE FROM team_members WHERE team_id=$1 AND user_id=$2`, teamID, userID); err != nil {
		a.adminControlFailure(c, "/admin/teams", "remove team member", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.adminControlFailure(c, "/admin/teams", "commit team member removal", err)
		return
	}
	adminOkRedirect(c, "/admin/teams", "Участник удалён из команды.")
}

func (a *App) adminControlTeamCaptain(c *gin.Context) {
	teamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	targetID, ok := teamPositiveID(c.PostForm("user_id"))
	if !ok {
		a.adminControlRedirect(c, "/admin/teams", "Выберите нового капитана.")
		return
	}
	ctx, tx, team, ok := a.adminControlBeginTeam(c, teamID, "назначить капитана")
	if !ok {
		return
	}
	defer tx.Rollback(ctx)
	var err error
	if targetID == team.CaptainID {
		a.adminControlRedirect(c, "/admin/teams", "Этот участник уже капитан.")
		return
	}
	if err = tx.QueryRow(ctx, `SELECT 1 FROM team_members WHERE team_id=$1 AND user_id=$2`, teamID, targetID).Scan(new(int)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			a.adminControlRedirect(c, "/admin/teams", "Капитаном можно назначить только текущего участника.")
			return
		}
		a.adminControlFailure(c, "/admin/teams", "load new captain", err)
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE teams SET captain_user_id=$1, updated_at=now() WHERE id=$2`, targetID, teamID); err != nil {
		a.adminControlFailure(c, "/admin/teams", "update team captain", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.adminControlFailure(c, "/admin/teams", "commit team captain update", err)
		return
	}
	adminOkRedirect(c, "/admin/teams", "Капитан команды изменён.")
}

func (a *App) adminControlTeamEligibility(c *gin.Context) {
	teamID, ok := adminID(c, "id")
	if !ok {
		return
	}
	setAllowed := c.PostForm("allowed") == "true"
	ctx, tx, _, ok := a.adminControlBeginTeam(c, teamID, "изменить eligibility override")
	if !ok {
		return
	}
	defer tx.Rollback(ctx)
	var err error
	if setAllowed {
		_, err = tx.Exec(ctx, `
			INSERT INTO team_eligibility_overrides (team_id, allowed, admin_user_id)
			VALUES ($1, true, $2)
			ON CONFLICT (team_id) DO UPDATE SET allowed=true,
			admin_user_id=EXCLUDED.admin_user_id, updated_at=now()`, teamID, CurrentUser(c).ID)
	} else {
		result, deleteErr := tx.Exec(ctx, `DELETE FROM team_eligibility_overrides WHERE team_id=$1`, teamID)
		if deleteErr != nil {
			err = deleteErr
		} else if result.RowsAffected() == 0 {
			a.adminControlRedirect(c, "/admin/teams", "У команды нет eligibility override.")
			return
		}
	}
	if err != nil {
		a.adminControlFailure(c, "/admin/teams", "update eligibility override", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.adminControlFailure(c, "/admin/teams", "commit eligibility override", err)
		return
	}
	adminOkRedirect(c, "/admin/teams", "Eligibility override обновлён.")
}

func (a *App) loadAdminControlTeams(ctx context.Context, jamFilter int64) ([]adminControlTeam, error) {
	rows, err := a.pool.Query(ctx, `
		SELECT t.id, t.jam_id, j.title, t.name, t.description, t.avatar_path,
		       t.captain_user_id, captain.username, j.max_team_size,
		       CASE WHEN i.team_id IS NULL THEN 'none' WHEN i.revoked_at IS NULL THEN 'active' ELSE 'revoked' END,
		       eo.allowed, COALESCE(override_admin.username, ''), COALESCE(eo.updated_at, eo.created_at),
		       EXISTS (
		           SELECT 1 FROM team_members em
		           JOIN questionnaires q ON q.jam_id=em.jam_id
		           JOIN questionnaire_responses r ON r.questionnaire_id=q.id AND r.revision=q.current_revision
		             AND r.user_id=em.user_id AND r.status='completed'
		           WHERE em.team_id=t.id
		       )
		FROM teams t
		JOIN jams j ON j.id=t.jam_id
		JOIN users captain ON captain.id=t.captain_user_id
		LEFT JOIN team_invites i ON i.team_id=t.id
		LEFT JOIN team_eligibility_overrides eo ON eo.team_id=t.id
		LEFT JOIN users override_admin ON override_admin.id=eo.admin_user_id
		WHERE $1=0 OR t.jam_id=$1
		ORDER BY j.created_at DESC, lower(t.name), t.id`, jamFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var teams []adminControlTeam
	teamByID := make(map[int64]int)
	for rows.Next() {
		var team adminControlTeam
		var overrideAdmin string
		var overrideAt *time.Time
		if err := rows.Scan(&team.ID, &team.JamID, &team.JamTitle, &team.Name, &team.Description,
			&team.AvatarPath, &team.CaptainID, &team.CaptainName, &team.MaxSize,
			&team.InviteState, &team.EligibilityOverride, &overrideAdmin, &overrideAt,
			&team.CompletedEligible); err != nil {
			return nil, err
		}
		if team.EligibilityOverride != nil && *team.EligibilityOverride {
			team.OverrideMeta = "установлен администратором"
			if overrideAdmin != "" {
				team.OverrideMeta += " " + overrideAdmin
			}
			if overrideAt != nil {
				location, locationErr := time.LoadLocation("Europe/Moscow")
				if locationErr != nil {
					location = time.FixedZone("Europe/Moscow", 3*60*60)
				}
				team.OverrideMeta += " (" + overrideAt.In(location).Format("02.01.2006 15:04 МСК") + ")"
			}
		}
		teams = append(teams, team)
		teamByID[team.ID] = len(teams) - 1
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	memberRows, err := a.pool.Query(ctx, `
		SELECT tm.team_id, u.id, u.username, t.captain_user_id=u.id
		FROM team_members tm JOIN users u ON u.id=tm.user_id JOIN teams t ON t.id=tm.team_id
		ORDER BY tm.team_id, (t.captain_user_id=u.id) DESC, lower(u.username), u.id`)
	if err != nil {
		return nil, err
	}
	defer memberRows.Close()
	for memberRows.Next() {
		var teamID int64
		var member adminControlMember
		if err := memberRows.Scan(&teamID, &member.ID, &member.Username, &member.Captain); err != nil {
			return nil, err
		}
		if index, exists := teamByID[teamID]; exists {
			teams[index].Members = append(teams[index].Members, member)
		}
	}
	return teams, memberRows.Err()
}

func (a *App) adminControlBeginTeam(c *gin.Context, teamID int64, action string) (context.Context, pgx.Tx, adminControlLockedTeam, bool) {
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.adminControlFailure(c, "/admin/teams", "begin admin team mutation", err)
		return ctx, nil, adminControlLockedTeam{}, false
	}
	team, err := adminControlLockTeam(ctx, tx, teamID)
	if err != nil {
		_ = tx.Rollback(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			c.AbortWithStatus(http.StatusNotFound)
		} else {
			a.adminControlFailure(c, "/admin/teams", action, err)
		}
		return ctx, nil, adminControlLockedTeam{}, false
	}
	return ctx, tx, team, true
}

func adminControlLockTeam(ctx context.Context, tx pgx.Tx, teamID int64) (adminControlLockedTeam, error) {
	var team adminControlLockedTeam
	err := tx.QueryRow(ctx, `
		SELECT t.id, t.jam_id, t.name, t.description, t.avatar_path, t.captain_user_id, j.max_team_size
		FROM teams t JOIN jams j ON j.id=t.jam_id
		WHERE t.id=$1 FOR UPDATE OF t FOR SHARE OF j`, teamID).Scan(
		&team.ID, &team.JamID, &team.Name, &team.Description, &team.AvatarPath, &team.CaptainID, &team.MaxSize)
	return team, err
}

func (a *App) adminControlRender(c *gin.Context, status int, name string, data adminControlPageData) {
	data.User = CurrentUser(c)
	data.CSRFToken = csrfToken(c)
	c.HTML(status, name, data)
}

func (a *App) adminControlRedirect(c *gin.Context, path, message string) {
	c.Redirect(http.StatusSeeOther, path+"?error="+url.QueryEscape(message))
}

func (a *App) adminControlMutationLoadError(c *gin.Context, path, operation string, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	a.adminControlFailure(c, path, operation, err)
}

func (a *App) adminControlFailure(c *gin.Context, path, operation string, err error) {
	a.logger.Error(operation, "error", err)
	a.adminControlRedirect(c, path, "Не удалось выполнить действие. Попробуйте позже.")
}
