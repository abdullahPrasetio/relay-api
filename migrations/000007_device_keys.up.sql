-- 000007_device_keys: per-device X25519 keys and end-to-end encrypted
-- workspace keys (phase 4.1).
--
-- A user registers one X25519 public key per device. Workspace secrets are
-- encrypted client-side with a 32-byte workspace key; that key is wrapped once
-- per recipient device and stored here. The wrap envelope is
--   ephemeral public key (32B) || AES-GCM nonce (12B) || ciphertext+tag (48B)
-- = 92 bytes, base64 on the wire and raw bytea at rest. The server never sees
-- the plaintext workspace key.
--
-- Versions are append-only: rotation inserts workspace_key_versions(version+1)
-- and the new per-device envelopes; older versions stay readable so a device
-- that has not synced yet can still unwrap.

CREATE TABLE IF NOT EXISTS device_keys (
    id         UUID        NOT NULL,
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- Base64 (standard encoding) of the 32-byte X25519 public key.
    public_key TEXT        NOT NULL,
    label      TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ NULL,
    PRIMARY KEY (id),
    CONSTRAINT device_keys_user_public_key_key UNIQUE (user_id, public_key)
);

-- Own-key listings and the workspace member fan-out both read by user.
CREATE INDEX IF NOT EXISTS idx_device_keys_user_created
    ON device_keys (user_id, created_at);

CREATE TABLE IF NOT EXISTS workspace_key_versions (
    workspace_id TEXT        NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    version      INT         NOT NULL CHECK (version > 0),
    created_by   UUID        NOT NULL REFERENCES users(id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, version)
);

CREATE TABLE IF NOT EXISTS workspace_keys (
    workspace_id  TEXT        NOT NULL,
    device_key_id UUID        NOT NULL REFERENCES device_keys(id) ON DELETE CASCADE,
    version       INT         NOT NULL,
    -- 92-byte wrapped workspace key (ephemeral pub || nonce || ciphertext+tag).
    wrapped_key   BYTEA       NOT NULL CHECK (octet_length(wrapped_key) = 92),
    created_by    UUID        NOT NULL REFERENCES users(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, device_key_id, version),
    CONSTRAINT workspace_keys_version_fkey
        FOREIGN KEY (workspace_id, version)
        REFERENCES workspace_key_versions (workspace_id, version)
        ON DELETE CASCADE
);

-- Reads are always "the wrapped keys for this workspace, for my devices".
CREATE INDEX IF NOT EXISTS idx_workspace_keys_device
    ON workspace_keys (device_key_id, workspace_id);
