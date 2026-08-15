CREATE TABLE product_bumps (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    product_id bigint NOT NULL,
    jam_id bigint NOT NULL,
    bump_count bigint NOT NULL DEFAULT 1 CHECK (bump_count > 0),
    last_bumped_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, product_id),
    CONSTRAINT product_bumps_product_same_jam_fk FOREIGN KEY (product_id, jam_id)
        REFERENCES products(id, jam_id) ON DELETE RESTRICT
);

CREATE INDEX product_bumps_product_idx ON product_bumps (product_id, jam_id);
