-- Readable URLs (#42): groups and teams are addressed by a slug instead of
-- their numeric ID. A group slug is unique site-wide, a team slug is unique
-- within its group. The application generates and validates slugs
-- (internal/service/slug.go); this migration only backfills existing rows
-- with the same rules: accents folded to ASCII, lowercase, runs of other
-- characters turned into one hyphen, and a numeric suffix on clashes. Keep the
-- reserved words in step with service.ReservedSlugs.
ALTER TABLE groups ADD COLUMN slug VARCHAR(64);
ALTER TABLE teams ADD COLUMN slug VARCHAR(64);

CREATE FUNCTION pg_temp.calbooker_slug_base(name TEXT, fallback TEXT) RETURNS TEXT AS $$
DECLARE
    base TEXT;
BEGIN
    base := lower(name);
    base := replace(replace(replace(base, 'æ', 'ae'), 'œ', 'oe'), 'ß', 'ss');
    base := translate(base,
        'áàâäãåāăąćčçďđéèêëēěęğíìîïīıłńňñóòôöõőøōřśšşßťúùûüűūůýÿžźż',
        'aaaaaaaaaccc' || 'ddeeeeeeeg' || 'iiiiiilnnn' || 'oooooooorssss' || 'tuuuuuuuyyzzz');
    base := btrim(regexp_replace(base, '[^a-z0-9]+', '-', 'g'), '-');
    base := btrim(left(base, 48), '-');
    IF base = '' THEN
        RETURN fallback;
    END IF;
    IF base ~ '^[0-9]+$' OR base IN (
        'new', 'edit', 'delete', 'create', 'join', 'leave', 'settings', 'members', 'teams', 'invites',
        'children', 'resources', 'seasons', 'events', 'calendar', 'coaches', 'plan', 'statistics',
        'training-plan', 'event-generation', 'archive', 'roster', 'admin'
    ) THEN
        RETURN fallback || '-' || base;
    END IF;
    RETURN base;
END;
$$ LANGUAGE plpgsql IMMUTABLE;

UPDATE groups g
SET slug = CASE WHEN ranked.position = 1 THEN ranked.base ELSE ranked.base || '-' || ranked.position END
FROM (
    SELECT id, pg_temp.calbooker_slug_base(name, 'group') AS base,
        row_number() OVER (PARTITION BY pg_temp.calbooker_slug_base(name, 'group') ORDER BY id) AS position
    FROM groups
) ranked
WHERE g.id = ranked.id;

UPDATE teams t
SET slug = CASE WHEN ranked.position = 1 THEN ranked.base ELSE ranked.base || '-' || ranked.position END
FROM (
    SELECT id, pg_temp.calbooker_slug_base(name, 'team') AS base,
        row_number() OVER (PARTITION BY group_id, pg_temp.calbooker_slug_base(name, 'team') ORDER BY id) AS position
    FROM teams
) ranked
WHERE t.id = ranked.id;

-- A numbered slug can still equal another row's own slug (a group named
-- "Lions 2"): make those unique by appending the ID.
UPDATE groups SET slug = slug || '-' || id
WHERE slug IN (SELECT slug FROM groups GROUP BY slug HAVING count(*) > 1);
UPDATE teams SET slug = slug || '-' || id
WHERE (group_id, slug) IN (SELECT group_id, slug FROM teams GROUP BY group_id, slug HAVING count(*) > 1);

DROP FUNCTION pg_temp.calbooker_slug_base(TEXT, TEXT);

ALTER TABLE groups
    ALTER COLUMN slug SET NOT NULL,
    ADD CONSTRAINT groups_slug_unique UNIQUE (slug),
    ADD CONSTRAINT groups_slug_format CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND slug !~ '^[0-9]+$');
ALTER TABLE teams
    ALTER COLUMN slug SET NOT NULL,
    ADD CONSTRAINT teams_group_slug_unique UNIQUE (group_id, slug),
    ADD CONSTRAINT teams_slug_format CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND slug !~ '^[0-9]+$');
