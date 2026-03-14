CREATE TABLE IF NOT EXISTS node_owners (
    node_id INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (node_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_node_owners_user_id ON node_owners(user_id);
CREATE INDEX IF NOT EXISTS idx_node_owners_node_id ON node_owners(node_id);
