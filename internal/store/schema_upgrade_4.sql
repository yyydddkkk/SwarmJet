-- Platform upgrades were removed; a leftover maintenance row must not block new tasks.
-- The platform_upgrades table is kept as history and is no longer written.
DROP TRIGGER IF EXISTS platform_upgrade_task_gate ON tasks;
DROP FUNCTION IF EXISTS reject_task_during_platform_upgrade();
