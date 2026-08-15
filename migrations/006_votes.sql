ALTER TABLE nominations
    ADD CONSTRAINT nominations_id_jam_unique UNIQUE (id, jam_id);

CREATE TABLE nomination_votes (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    nomination_id bigint NOT NULL,
    product_id bigint NOT NULL,
    jam_id bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, nomination_id),
    CONSTRAINT nomination_votes_nomination_same_jam_fk
        FOREIGN KEY (nomination_id, jam_id)
        REFERENCES nominations(id, jam_id) ON DELETE RESTRICT,
    CONSTRAINT nomination_votes_product_same_jam_fk
        FOREIGN KEY (product_id, jam_id)
        REFERENCES products(id, jam_id) ON DELETE RESTRICT
);

CREATE INDEX nomination_votes_counts_idx
ON nomination_votes (jam_id, nomination_id, product_id);
