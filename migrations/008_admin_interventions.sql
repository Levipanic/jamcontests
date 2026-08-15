ALTER TABLE nomination_votes
    ADD COLUMN id bigint GENERATED ALWAYS AS IDENTITY,
    ADD COLUMN invalidated_at timestamptz,
    ADD COLUMN invalidated_by bigint REFERENCES users(id) ON DELETE RESTRICT,
    ADD COLUMN invalidation_reason text,
    DROP CONSTRAINT nomination_votes_pkey,
    ADD CONSTRAINT nomination_votes_pkey PRIMARY KEY (id),
    ADD CONSTRAINT nomination_votes_invalidation_state_check CHECK (
        (invalidated_at IS NULL AND invalidated_by IS NULL AND invalidation_reason IS NULL)
        OR (invalidated_at IS NOT NULL AND invalidated_by IS NOT NULL
            AND char_length(btrim(invalidation_reason)) BETWEEN 3 AND 1000)
    );

DROP INDEX nomination_votes_counts_idx;

CREATE UNIQUE INDEX nomination_votes_active_selection_unique
ON nomination_votes (user_id, nomination_id) WHERE invalidated_at IS NULL;

CREATE INDEX nomination_votes_active_counts_idx
ON nomination_votes (jam_id, nomination_id, product_id) WHERE invalidated_at IS NULL;

ALTER TABLE product_bumps
    ADD COLUMN id bigint GENERATED ALWAYS AS IDENTITY,
    ADD COLUMN invalidated_count bigint NOT NULL DEFAULT 0,
    ADD COLUMN invalidated_at timestamptz,
    ADD COLUMN invalidated_by bigint REFERENCES users(id) ON DELETE RESTRICT,
    ADD COLUMN invalidation_reason text,
    ADD CONSTRAINT product_bumps_id_unique UNIQUE (id),
    ADD CONSTRAINT product_bumps_invalidated_count_check CHECK (
        invalidated_count BETWEEN 0 AND bump_count
    ),
    ADD CONSTRAINT product_bumps_invalidation_state_check CHECK (
        (invalidated_count = 0 AND invalidated_at IS NULL AND invalidated_by IS NULL AND invalidation_reason IS NULL)
        OR (invalidated_count > 0 AND invalidated_at IS NOT NULL AND invalidated_by IS NOT NULL
            AND char_length(btrim(invalidation_reason)) BETWEEN 3 AND 1000)
    );
