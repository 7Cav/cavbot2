# Renaming a store column for a clearer name

Sometimes a column's name in the bot's Postgres stops saying what the column
holds. Renaming it just to make the name clearer is out of scope.

The case that raised it is `foxhole_records.approved`. It holds the External
approval. It dates from when an approval could only mean External, and since
#673 it sits beside `approved_internal`. A maintainer reading the schema has to
look up which role `approved` holds.

## Why this is out of scope

No member or Foxhole manager sees a column name. Only a maintainer writing SQL
does, and the store keeps all of it in `store/postgres.go`. Three of the four
functions there that name `approved` carry a comment saying it holds the
External approval. Code outside the store reads approvals through
`FoxholeApprovals{Internal, External}`, which names the role.

A rename costs far more than the name is worth. Every migration has to work
with the previous release, because a rollback runs the older image against the
newer schema. The `store/store.go` package doc and spec #285 set that rule.
The previous release queries
the old name, so `ALTER TABLE ... RENAME COLUMN` breaks it. A compatible rename
takes two migrations, one release apart:

```sql
-- Release N: the new column, filled from the old one. The code writes both.
ALTER TABLE foxhole_records ADD COLUMN approved_external BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE foxhole_records SET approved_external = approved;

-- Release N+1: the old name goes.
ALTER TABLE foxhole_records DROP COLUMN approved;
```

The first step leaves a stale copy behind. After a rollback, release N-1 writes
only `approved`. When release N comes back, goose doesn't run the copy again,
so `approved_external` holds the old value for every record changed in
between. A sync trigger would fix that, and so would a third release that
reads the old column until the drop. Either way it would be the store's first
migration that drops a column outside a Down section, and all of that work
leaves the panel and the commands as they were.

## Prior requests

- #674: "foxhole_records.approved does not say it holds the External approval"
