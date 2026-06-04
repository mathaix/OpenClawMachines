ALTER TABLE browser_vms
    ADD COLUMN desired_rootfs_manifest TEXT,
    ADD COLUMN desired_rootfs_version TEXT;

