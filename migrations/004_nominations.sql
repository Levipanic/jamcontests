ALTER TABLE products
    ADD CONSTRAINT products_id_jam_unique UNIQUE (id, jam_id),
    ADD CONSTRAINT products_id_team_jam_unique UNIQUE (id, team_id, jam_id);

CREATE TABLE nominations (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    jam_id bigint NOT NULL REFERENCES jams(id) ON DELETE RESTRICT,
    kind text NOT NULL CHECK (kind IN ('team', 'curator')),
    title varchar(160) NOT NULL CHECK (
        title = btrim(title)
        AND char_length(title) BETWEEN 1 AND 160
    ),
    author_team_id bigint,
    product_id bigint,
    withdrawn_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT nominations_team_same_jam_fk FOREIGN KEY (author_team_id, jam_id)
        REFERENCES teams(id, jam_id) ON DELETE RESTRICT,
    CONSTRAINT nominations_product_same_jam_fk FOREIGN KEY (product_id, jam_id)
        REFERENCES products(id, jam_id) ON DELETE RESTRICT,
    CONSTRAINT nominations_team_product_fk FOREIGN KEY (product_id, author_team_id, jam_id)
        REFERENCES products(id, team_id, jam_id) ON DELETE RESTRICT,
    CHECK (
        (kind = 'team' AND author_team_id IS NOT NULL AND product_id IS NOT NULL)
        OR (kind = 'curator' AND author_team_id IS NULL AND product_id IS NULL)
    )
);

CREATE UNIQUE INDEX nominations_team_product_history_unique
ON nominations (jam_id, product_id)
WHERE kind = 'team';

CREATE UNIQUE INDEX nominations_team_author_history_unique
ON nominations (jam_id, author_team_id)
WHERE kind = 'team';

CREATE INDEX nominations_active_jam_idx ON nominations (jam_id, created_at, id)
WHERE withdrawn_at IS NULL;
