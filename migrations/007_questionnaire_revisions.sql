ALTER TABLE questionnaires
    ADD COLUMN current_revision integer NOT NULL DEFAULT 1 CHECK (current_revision > 0);

ALTER TABLE questionnaire_questions
    ADD COLUMN revision integer NOT NULL DEFAULT 1 CHECK (revision > 0);

ALTER TABLE questionnaire_responses
    ADD COLUMN revision integer NOT NULL DEFAULT 1 CHECK (revision > 0);

ALTER TABLE questionnaire_questions
    DROP CONSTRAINT questionnaire_questions_questionnaire_id_position_key,
    ADD CONSTRAINT questionnaire_questions_revision_position_unique
        UNIQUE (questionnaire_id, revision, position);

ALTER TABLE questionnaire_responses
    DROP CONSTRAINT questionnaire_responses_questionnaire_id_user_id_key,
    ADD CONSTRAINT questionnaire_responses_revision_user_unique
        UNIQUE (questionnaire_id, revision, user_id);

ALTER TABLE questionnaire_response_history
    ADD COLUMN admin_audit_log_id bigint REFERENCES admin_audit_log(id) ON DELETE RESTRICT,
    ADD CONSTRAINT questionnaire_history_reset_audit_check CHECK (
        (event = 'admin_reset' AND admin_audit_log_id IS NOT NULL)
        OR (event <> 'admin_reset' AND admin_audit_log_id IS NULL)
    );

CREATE FUNCTION enforce_questionnaire_revision_advance() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.current_revision <> OLD.current_revision + 1 THEN
        RAISE EXCEPTION 'questionnaire revision must advance by exactly one';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER questionnaires_revision_advance
BEFORE UPDATE OF current_revision ON questionnaires
FOR EACH ROW EXECUTE FUNCTION enforce_questionnaire_revision_advance();

CREATE INDEX questionnaire_questions_revision_idx
ON questionnaire_questions (questionnaire_id, revision, position);

CREATE INDEX questionnaire_responses_revision_idx
ON questionnaire_responses (questionnaire_id, revision, user_id);

CREATE FUNCTION prevent_questionnaire_history_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'questionnaire response history is append-only';
END;
$$;

CREATE TRIGGER questionnaire_history_immutable
BEFORE UPDATE OR DELETE ON questionnaire_response_history
FOR EACH ROW EXECUTE FUNCTION prevent_questionnaire_history_mutation();

CREATE TRIGGER questionnaire_history_no_truncate
BEFORE TRUNCATE ON questionnaire_response_history
FOR EACH STATEMENT EXECUTE FUNCTION prevent_questionnaire_history_mutation();

CREATE TRIGGER questionnaires_no_truncate
BEFORE TRUNCATE ON questionnaires
FOR EACH STATEMENT EXECUTE FUNCTION prevent_questionnaire_history_mutation();

CREATE TRIGGER questionnaire_questions_no_truncate
BEFORE TRUNCATE ON questionnaire_questions
FOR EACH STATEMENT EXECUTE FUNCTION prevent_questionnaire_history_mutation();

CREATE TRIGGER questionnaire_options_no_truncate
BEFORE TRUNCATE ON questionnaire_options
FOR EACH STATEMENT EXECUTE FUNCTION prevent_questionnaire_history_mutation();

CREATE TRIGGER questionnaire_responses_no_truncate
BEFORE TRUNCATE ON questionnaire_responses
FOR EACH STATEMENT EXECUTE FUNCTION prevent_questionnaire_history_mutation();

CREATE TRIGGER questionnaire_text_answers_no_truncate
BEFORE TRUNCATE ON questionnaire_text_answers
FOR EACH STATEMENT EXECUTE FUNCTION prevent_questionnaire_history_mutation();

CREATE TRIGGER questionnaire_selected_options_no_truncate
BEFORE TRUNCATE ON questionnaire_selected_options
FOR EACH STATEMENT EXECUTE FUNCTION prevent_questionnaire_history_mutation();

CREATE FUNCTION ensure_current_question_revision() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    questionnaire_id_value bigint;
    revision_value integer;
BEGIN
	IF TG_OP = 'UPDATE' AND NOT EXISTS (
		SELECT 1 FROM questionnaires
		WHERE id = OLD.questionnaire_id AND current_revision = OLD.revision
	) THEN
		RAISE EXCEPTION 'historical questionnaire question is immutable';
	END IF;
    IF TG_OP = 'DELETE' THEN
        questionnaire_id_value := OLD.questionnaire_id;
        revision_value := OLD.revision;
    ELSE
        questionnaire_id_value := NEW.questionnaire_id;
        revision_value := NEW.revision;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM questionnaires
        WHERE id = questionnaire_id_value AND current_revision = revision_value
    ) THEN
        RAISE EXCEPTION 'historical questionnaire question is immutable';
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER questionnaire_questions_current_revision
BEFORE INSERT OR UPDATE OR DELETE ON questionnaire_questions
FOR EACH ROW EXECUTE FUNCTION ensure_current_question_revision();

CREATE FUNCTION ensure_current_option_revision() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    question_id_value bigint;
BEGIN
	IF TG_OP = 'UPDATE' AND NOT EXISTS (
		SELECT 1
		FROM questionnaire_questions question
		JOIN questionnaires questionnaire ON questionnaire.id = question.questionnaire_id
		WHERE question.id = OLD.question_id
		  AND question.revision = questionnaire.current_revision
	) THEN
		RAISE EXCEPTION 'historical questionnaire option is immutable';
	END IF;
    IF TG_OP = 'DELETE' THEN question_id_value := OLD.question_id;
    ELSE question_id_value := NEW.question_id;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM questionnaire_questions question
        JOIN questionnaires questionnaire ON questionnaire.id = question.questionnaire_id
        WHERE question.id = question_id_value
          AND question.revision = questionnaire.current_revision
    ) THEN
        RAISE EXCEPTION 'historical questionnaire option is immutable';
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER questionnaire_options_current_revision
BEFORE INSERT OR UPDATE OR DELETE ON questionnaire_options
FOR EACH ROW EXECUTE FUNCTION ensure_current_option_revision();

CREATE FUNCTION ensure_current_response_revision() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    questionnaire_id_value bigint;
    revision_value integer;
BEGIN
	IF TG_OP = 'UPDATE' AND NOT EXISTS (
		SELECT 1 FROM questionnaires
		WHERE id = OLD.questionnaire_id AND current_revision = OLD.revision
	) THEN
		RAISE EXCEPTION 'historical questionnaire response is immutable';
	END IF;
    IF TG_OP = 'DELETE' THEN
        questionnaire_id_value := OLD.questionnaire_id;
        revision_value := OLD.revision;
    ELSE
        questionnaire_id_value := NEW.questionnaire_id;
        revision_value := NEW.revision;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM questionnaires
        WHERE id = questionnaire_id_value AND current_revision = revision_value
    ) THEN
        RAISE EXCEPTION 'historical questionnaire response is immutable';
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER questionnaire_responses_current_revision
BEFORE INSERT OR UPDATE OR DELETE ON questionnaire_responses
FOR EACH ROW EXECUTE FUNCTION ensure_current_response_revision();

CREATE FUNCTION ensure_current_answer_revision() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    response_id_value bigint;
    question_id_value bigint;
BEGIN
	IF TG_OP = 'UPDATE' AND NOT EXISTS (
		SELECT 1
		FROM questionnaire_responses response
		JOIN questionnaire_questions question ON question.id = OLD.question_id
		JOIN questionnaires questionnaire ON questionnaire.id = response.questionnaire_id
		WHERE response.id = OLD.response_id
		  AND question.questionnaire_id = response.questionnaire_id
		  AND question.revision = response.revision
		  AND response.revision = questionnaire.current_revision
	) THEN
		RAISE EXCEPTION 'historical questionnaire answer is immutable';
	END IF;
    IF TG_OP = 'DELETE' THEN
        response_id_value := OLD.response_id;
        question_id_value := OLD.question_id;
    ELSE
        response_id_value := NEW.response_id;
        question_id_value := NEW.question_id;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM questionnaire_responses response
        JOIN questionnaire_questions question ON question.id = question_id_value
        JOIN questionnaires questionnaire ON questionnaire.id = response.questionnaire_id
        WHERE response.id = response_id_value
          AND question.questionnaire_id = response.questionnaire_id
          AND question.revision = response.revision
          AND response.revision = questionnaire.current_revision
    ) THEN
        RAISE EXCEPTION 'historical questionnaire answer is immutable';
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER questionnaire_text_answers_current_revision
BEFORE INSERT OR UPDATE OR DELETE ON questionnaire_text_answers
FOR EACH ROW EXECUTE FUNCTION ensure_current_answer_revision();

CREATE TRIGGER questionnaire_selected_options_current_revision
BEFORE INSERT OR UPDATE OR DELETE ON questionnaire_selected_options
FOR EACH ROW EXECUTE FUNCTION ensure_current_answer_revision();
