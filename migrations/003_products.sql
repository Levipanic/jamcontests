CREATE TABLE products (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    jam_id bigint NOT NULL,
    team_id bigint NOT NULL,
    title varchar(200) NOT NULL DEFAULT '' CHECK (
        title = btrim(title)
        AND char_length(title) <= 200
    ),
    result_url varchar(2048) NOT NULL DEFAULT '' CHECK (
        result_url = btrim(result_url)
        AND char_length(result_url) <= 2048
    ),
    description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 5000),
    commentary_url varchar(2048) CHECK (
        commentary_url IS NULL OR (
            commentary_url = btrim(commentary_url)
            AND char_length(commentary_url) BETWEEN 1 AND 2048
        )
    ),
    notes text NOT NULL DEFAULT '' CHECK (char_length(notes) <= 5000),
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'final')),
    finalized_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT products_team_jam_unique UNIQUE (team_id, jam_id),
    CONSTRAINT products_team_same_jam_fk FOREIGN KEY (team_id, jam_id)
        REFERENCES teams(id, jam_id) ON DELETE RESTRICT,
    CHECK (
        (status = 'draft' AND finalized_at IS NULL)
        OR (
            status = 'final'
            AND finalized_at IS NOT NULL
            AND char_length(title) >= 1
            AND char_length(result_url) >= 1
        )
    )
);

CREATE INDEX products_public_jam_idx ON products (jam_id, created_at, id)
WHERE status = 'final';
