CREATE TABLE users (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username varchar(40) NOT NULL CHECK (char_length(btrim(username)) BETWEEN 3 AND 40),
    email varchar(254) CHECK (email IS NULL OR char_length(email) BETWEEN 3 AND 254),
    password_hash text NOT NULL,
    role text NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_username_ci_unique ON users (lower(username));
CREATE UNIQUE INDEX users_email_ci_unique ON users (lower(email)) WHERE email IS NOT NULL;

CREATE TABLE sessions (
    token_hash bytea PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

CREATE TABLE admin_audit_log (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    admin_user_id bigint NOT NULL REFERENCES users(id),
    action text NOT NULL CHECK (action <> ''),
    entity_type text NOT NULL CHECK (entity_type <> ''),
    entity_id bigint,
    reason text NOT NULL CHECK (char_length(btrim(reason)) BETWEEN 3 AND 1000),
    before_data jsonb,
    after_data jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE FUNCTION prevent_audit_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'admin audit log is append-only';
END;
$$;
CREATE TRIGGER admin_audit_immutable
BEFORE UPDATE OR DELETE ON admin_audit_log
FOR EACH ROW EXECUTE FUNCTION prevent_audit_mutation();
CREATE TRIGGER admin_audit_no_truncate
BEFORE TRUNCATE ON admin_audit_log
FOR EACH STATEMENT EXECUTE FUNCTION prevent_audit_mutation();

CREATE TABLE jams (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title varchar(160) NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 160),
    description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 10000),
    rules text NOT NULL DEFAULT '' CHECK (char_length(rules) <= 20000),
    visibility text NOT NULL DEFAULT 'draft' CHECK (visibility IN ('draft', 'published')),
    submission_starts_at timestamptz NOT NULL,
    evaluation_starts_at timestamptz NOT NULL,
    voting_starts_at timestamptz NOT NULL,
    finishes_at timestamptz NOT NULL,
    status_override text CHECK (status_override IS NULL OR status_override IN ('upcoming', 'submission', 'evaluation', 'voting', 'finished')),
    max_team_size integer NOT NULL CHECK (max_team_size BETWEEN 1 AND 100),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (submission_starts_at < evaluation_starts_at),
    CHECK (evaluation_starts_at < voting_starts_at),
    CHECK (voting_starts_at < finishes_at)
);

CREATE TABLE questionnaires (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    jam_id bigint NOT NULL UNIQUE REFERENCES jams(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE questionnaire_questions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    questionnaire_id bigint NOT NULL REFERENCES questionnaires(id) ON DELETE RESTRICT,
    type text NOT NULL CHECK (type IN ('short_text', 'single_choice', 'multiple_choice')),
    prompt varchar(500) NOT NULL CHECK (char_length(btrim(prompt)) BETWEEN 1 AND 500),
    hint varchar(1000),
    required boolean NOT NULL DEFAULT false,
    text_limit integer,
    selection_limit integer,
    position integer NOT NULL CHECK (position >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (questionnaire_id, position),
    CHECK ((type = 'short_text' AND text_limit IS NOT NULL AND text_limit BETWEEN 1 AND 5000 AND selection_limit IS NULL)
        OR (type = 'single_choice' AND text_limit IS NULL AND selection_limit IS NULL)
        OR (type = 'multiple_choice' AND text_limit IS NULL AND selection_limit IS NOT NULL AND selection_limit >= 1))
);

CREATE TABLE questionnaire_options (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    question_id bigint NOT NULL REFERENCES questionnaire_questions(id) ON DELETE RESTRICT,
    label varchar(300) NOT NULL CHECK (char_length(btrim(label)) BETWEEN 1 AND 300),
    position integer NOT NULL CHECK (position >= 0),
    UNIQUE (question_id, position),
    UNIQUE (id, question_id)
);

CREATE TABLE questionnaire_responses (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    questionnaire_id bigint NOT NULL REFERENCES questionnaires(id) ON DELETE RESTRICT,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'completed')),
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (questionnaire_id, user_id),
    CHECK ((status = 'draft' AND completed_at IS NULL) OR (status = 'completed' AND completed_at IS NOT NULL))
);

CREATE TABLE questionnaire_text_answers (
    response_id bigint NOT NULL REFERENCES questionnaire_responses(id) ON DELETE RESTRICT,
    question_id bigint NOT NULL REFERENCES questionnaire_questions(id) ON DELETE RESTRICT,
    value text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (response_id, question_id)
);

CREATE TABLE questionnaire_selected_options (
    response_id bigint NOT NULL REFERENCES questionnaire_responses(id) ON DELETE RESTRICT,
    question_id bigint NOT NULL REFERENCES questionnaire_questions(id) ON DELETE RESTRICT,
    option_id bigint NOT NULL,
    PRIMARY KEY (response_id, question_id, option_id),
    FOREIGN KEY (option_id, question_id) REFERENCES questionnaire_options(id, question_id) ON DELETE RESTRICT
);

CREATE TABLE questionnaire_response_history (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    response_id bigint NOT NULL REFERENCES questionnaire_responses(id) ON DELETE RESTRICT,
    event text NOT NULL CHECK (event IN ('completed', 'returned_to_draft', 'admin_reset')),
    snapshot jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE teams (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    jam_id bigint NOT NULL REFERENCES jams(id) ON DELETE RESTRICT,
    name varchar(100) NOT NULL CHECK (char_length(btrim(name)) BETWEEN 2 AND 100),
    description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 3000),
    avatar_path text,
    captain_user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, jam_id)
);
CREATE UNIQUE INDEX teams_name_per_jam_ci_unique ON teams (jam_id, lower(name));

CREATE TABLE team_members (
    team_id bigint NOT NULL,
    jam_id bigint NOT NULL,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    is_product_editor boolean NOT NULL DEFAULT false,
    joined_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, user_id),
    UNIQUE (jam_id, user_id),
    FOREIGN KEY (team_id, jam_id) REFERENCES teams(id, jam_id) ON DELETE RESTRICT
);

ALTER TABLE questionnaire_responses
ADD COLUMN team_id_at_start bigint NOT NULL REFERENCES teams(id) ON DELETE RESTRICT;

CREATE FUNCTION validate_questionnaire_text_answer() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM questionnaire_responses r
        JOIN questionnaire_questions q ON q.id = NEW.question_id
        WHERE r.id = NEW.response_id
          AND r.questionnaire_id = q.questionnaire_id
          AND q.type = 'short_text'
    ) THEN
        RAISE EXCEPTION 'text answer must belong to a short-text question in the same questionnaire';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER questionnaire_text_answer_integrity
BEFORE INSERT OR UPDATE ON questionnaire_text_answers
FOR EACH ROW EXECUTE FUNCTION validate_questionnaire_text_answer();

CREATE FUNCTION validate_questionnaire_selected_option() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM questionnaire_responses r
        JOIN questionnaire_questions q ON q.id = NEW.question_id
        WHERE r.id = NEW.response_id
          AND r.questionnaire_id = q.questionnaire_id
          AND q.type IN ('single_choice', 'multiple_choice')
    ) THEN
        RAISE EXCEPTION 'selected option must belong to a choice question in the same questionnaire';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER questionnaire_selected_option_integrity
BEFORE INSERT OR UPDATE ON questionnaire_selected_options
FOR EACH ROW EXECUTE FUNCTION validate_questionnaire_selected_option();

CREATE FUNCTION check_team_captain_from_team() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM teams t
        WHERE t.id = NEW.id
          AND NOT EXISTS (
              SELECT 1 FROM team_members tm
              WHERE tm.team_id = t.id AND tm.user_id = t.captain_user_id
          )
    ) THEN
        RAISE EXCEPTION 'team captain must be a current team member';
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER teams_captain_is_member
AFTER INSERT OR UPDATE OF captain_user_id ON teams
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION check_team_captain_from_team();

CREATE FUNCTION check_team_captain_from_member() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    checked_team_id bigint;
BEGIN
    checked_team_id := OLD.team_id;
    IF EXISTS (
        SELECT 1 FROM teams t
        WHERE t.id = checked_team_id
          AND NOT EXISTS (
              SELECT 1 FROM team_members tm
              WHERE tm.team_id = t.id AND tm.user_id = t.captain_user_id
          )
    ) THEN
        RAISE EXCEPTION 'team captain must be a current team member';
    END IF;
    IF TG_OP = 'UPDATE' AND NEW.team_id <> OLD.team_id AND EXISTS (
        SELECT 1 FROM teams t
        WHERE t.id = NEW.team_id
          AND NOT EXISTS (
              SELECT 1 FROM team_members tm
              WHERE tm.team_id = t.id AND tm.user_id = t.captain_user_id
          )
    ) THEN
        RAISE EXCEPTION 'team captain must be a current team member';
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER members_keep_captain
AFTER DELETE OR UPDATE ON team_members
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION check_team_captain_from_member();

CREATE TABLE team_invites (
    team_id bigint PRIMARY KEY REFERENCES teams(id) ON DELETE RESTRICT,
    token_hash bytea NOT NULL UNIQUE,
    created_by bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);

CREATE TABLE team_eligibility_overrides (
    team_id bigint PRIMARY KEY REFERENCES teams(id) ON DELETE RESTRICT,
    allowed boolean NOT NULL,
    reason text NOT NULL CHECK (char_length(btrim(reason)) BETWEEN 3 AND 1000),
    admin_user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
