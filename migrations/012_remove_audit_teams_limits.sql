-- Product decision: administrative audit journal and mandatory reasons are
-- removed. Historical response records keep their snapshots but no longer
-- reference the audit log. Manual team dissolution keeps questionnaire
-- answers alive by detaching them from the deleted team. Team profile limits
-- are tightened to prevent layout abuse.

ALTER TABLE questionnaire_response_history
    DROP CONSTRAINT questionnaire_history_reset_audit_check,
    DROP COLUMN admin_audit_log_id;

ALTER TABLE questionnaire_responses
    DROP CONSTRAINT questionnaire_responses_team_id_at_start_fkey,
    ALTER COLUMN team_id_at_start DROP NOT NULL,
    ADD CONSTRAINT questionnaire_responses_team_id_at_start_fkey
        FOREIGN KEY (team_id_at_start) REFERENCES teams(id) ON DELETE SET NULL;

DROP TABLE admin_audit_log;

ALTER TABLE team_eligibility_overrides
    DROP CONSTRAINT team_eligibility_overrides_reason_check,
    ALTER COLUMN reason SET DEFAULT '';

ALTER TABLE teams
    DROP CONSTRAINT teams_name_check,
    ADD CONSTRAINT teams_name_check CHECK (char_length(btrim(name)) BETWEEN 2 AND 60),
    DROP CONSTRAINT teams_description_check,
    ADD CONSTRAINT teams_description_check CHECK (char_length(description) <= 1000);