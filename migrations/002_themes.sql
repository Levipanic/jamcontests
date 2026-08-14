CREATE TABLE jam_themes (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    jam_id bigint NOT NULL REFERENCES jams(id) ON DELETE RESTRICT,
    phrase varchar(160) NOT NULL CHECK (
        phrase = btrim(phrase)
        AND char_length(phrase) BETWEEN 1 AND 160
    ),
    copied_from_theme_id bigint REFERENCES jam_themes(id) ON DELETE RESTRICT,
    withdrawn_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, jam_id)
);

CREATE UNIQUE INDEX jam_themes_active_phrase_ci_unique
ON jam_themes (jam_id, lower(phrase))
WHERE withdrawn_at IS NULL;

CREATE TABLE team_theme_selections (
    team_id bigint PRIMARY KEY,
    jam_id bigint NOT NULL,
    theme_id bigint NOT NULL,
    selected_by_user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (team_id, jam_id) REFERENCES teams(id, jam_id) ON DELETE RESTRICT,
    FOREIGN KEY (theme_id, jam_id) REFERENCES jam_themes(id, jam_id) ON DELETE RESTRICT
);

CREATE INDEX team_theme_selections_theme_id_idx ON team_theme_selections (theme_id);
