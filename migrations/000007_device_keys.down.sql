-- 000007_device_keys: revert the device key registry and wrapped keys.

DROP TABLE IF EXISTS workspace_keys;
DROP TABLE IF EXISTS workspace_key_versions;
DROP TABLE IF EXISTS device_keys;
