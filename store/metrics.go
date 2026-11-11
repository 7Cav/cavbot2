package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/7cav/cavbot2/metrics"
)

// openVoiceSession opens a session for a user with none open. The partial
// unique index keeps a user to one open session, so an open one wins.
const openVoiceSession = `
	INSERT INTO metric_voice_sessions (user_id, channel_id, category_id, role_ids, started_at)
	VALUES ($1, $2, $3, COALESCE($4::text[], '{}'), $5)
	ON CONFLICT (user_id) WHERE ended_at IS NULL DO NOTHING`

// StartMetrics records now as the start of collection unless a start is
// already recorded, and returns the state.
func (p *Postgres) StartMetrics(ctx context.Context, now time.Time) (metrics.State, error) {
	if _, err := p.db.ExecContext(ctx,
		`INSERT INTO metric_state (started_at) VALUES ($1) ON CONFLICT DO NOTHING`, now); err != nil {
		return metrics.State{}, fmt.Errorf("start metrics: %w", err)
	}
	return p.MetricsState(ctx)
}

// MetricsState is the zero State before StartMetrics.
func (p *Postgres) MetricsState(ctx context.Context) (metrics.State, error) {
	var (
		state      metrics.State
		backfilled sql.NullTime
	)
	err := p.db.QueryRowContext(ctx, `SELECT started_at, backfilled_at FROM metric_state`).
		Scan(&state.StartedAt, &backfilled)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return metrics.State{}, nil
	case err != nil:
		return metrics.State{}, fmt.Errorf("read metrics state: %w", err)
	}
	state.BackfilledAt = backfilled.Time
	return state, nil
}

func (p *Postgres) MarkMetricsBackfilled(ctx context.Context, at time.Time) error {
	if _, err := p.db.ExecContext(ctx, `UPDATE metric_state SET backfilled_at = $1`, at); err != nil {
		return fmt.Errorf("mark metrics backfilled: %w", err)
	}
	return nil
}

// RecordMessages stores msgs in one transaction. A message already stored
// is skipped, so a backfill that overlaps live collection or reruns adds
// nothing twice.
func (p *Postgres) RecordMessages(ctx context.Context, msgs []metrics.Message) error {
	err := p.inTx(ctx, func(tx *sql.Tx) error {
		for _, m := range msgs {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO metric_messages (message_id, user_id, category_id, sent_at, addressed_ids)
				VALUES ($1, $2, $3, $4, COALESCE($5::text[], '{}'))
				ON CONFLICT DO NOTHING`,
				m.ID, m.UserID, m.CategoryID, m.SentAt, m.Addressed); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("record %d messages: %w", len(msgs), err)
	}
	return nil
}

// SetVoicePresence keeps the user's open session when it is in vp's channel.
// Otherwise it ends the open session at at, and opens one at at when vp
// names a channel.
func (p *Postgres) SetVoicePresence(ctx context.Context, vp metrics.VoicePresence, at time.Time) error {
	err := p.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE metric_voice_sessions SET ended_at = GREATEST(started_at, $3)
			WHERE user_id = $1 AND ended_at IS NULL AND (channel_id <> $2 OR $2 = '')`,
			vp.UserID, vp.ChannelID, at); err != nil {
			return err
		}
		if vp.ChannelID == "" {
			return nil
		}
		_, err := tx.ExecContext(ctx, openVoiceSession, vp.UserID, vp.ChannelID, vp.CategoryID, vp.RoleIDs, at)
		return err
	})
	if err != nil {
		return fmt.Errorf("set voice presence of %q: %w", vp.UserID, err)
	}
	return nil
}

// SyncVoicePresence makes the open sessions match present: it ends at at
// every open session whose user and channel are not in present, and opens
// the missing ones at at. A nil present ends every open session.
func (p *Postgres) SyncVoicePresence(ctx context.Context, present []metrics.VoicePresence, at time.Time) error {
	users := make([]string, len(present))
	channels := make([]string, len(present))
	for i, vp := range present {
		users[i], channels[i] = vp.UserID, vp.ChannelID
	}
	err := p.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE metric_voice_sessions s SET ended_at = GREATEST(s.started_at, $3)
			WHERE s.ended_at IS NULL AND NOT EXISTS (
				SELECT 1 FROM unnest($1::text[], $2::text[]) AS p(user_id, channel_id)
				WHERE p.user_id = s.user_id AND p.channel_id = s.channel_id)`,
			users, channels, at); err != nil {
			return err
		}
		for _, vp := range present {
			if vp.ChannelID == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, openVoiceSession, vp.UserID, vp.ChannelID, vp.CategoryID, vp.RoleIDs, at); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("sync voice presence of %d members: %w", len(present), err)
	}
	return nil
}

func (p *Postgres) RecordMemberEvent(ctx context.Context, e metrics.MemberEvent) error {
	if _, err := p.db.ExecContext(ctx,
		`INSERT INTO metric_member_events (user_id, kind, at) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		e.UserID, string(e.Kind), e.At); err != nil {
		return fmt.Errorf("record %s of %q: %w", e.Kind, e.UserID, err)
	}
	return nil
}

// rollUpWeeks writes the totals of each week from $1 to $2 (Mondays) for the
// sections $3 and the server, whose category_id is empty. $4 and $5 pair a
// section's category with each of its roles. Section voice counts by role
// for a section with roles, by channel for one without. Messages and active
// count by channel. Weeks step by 168 hours, as days would follow the
// session time zone's DST.
const rollUpWeeks = `
WITH weeks AS (
	SELECT start FROM generate_series($1::timestamptz, $2::timestamptz - interval '1 second', interval '168 hours') AS start
),
sec AS (
	SELECT s.category_id, ARRAY(
		SELECT r.role_id FROM unnest($4::text[], $5::text[]) AS r(category_id, role_id)
		WHERE r.category_id = s.category_id
	) AS role_ids
	FROM unnest($3::text[]) AS s(category_id)
),
msg AS (
	SELECT w.start, m.category_id, m.user_id
	FROM weeks w JOIN metric_messages m ON m.sent_at >= w.start AND m.sent_at < w.start + interval '168 hours'
),
vs AS (
	SELECT w.start, v.category_id, v.role_ids, v.user_id,
		EXTRACT(EPOCH FROM LEAST(COALESCE(v.ended_at, 'infinity'), w.start + interval '168 hours')
			- GREATEST(v.started_at, w.start)) AS secs
	FROM weeks w JOIN metric_voice_sessions v
		ON v.started_at < w.start + interval '168 hours' AND COALESCE(v.ended_at, 'infinity') > w.start
),
counted AS (
	SELECT start, CASE WHEN GROUPING(category_id) = 1 THEN '' ELSE category_id END AS category_id,
		count(*) FILTER (WHERE is_message) AS messages, count(DISTINCT user_id) AS active
	FROM (
		SELECT start, category_id, user_id, true AS is_message FROM msg
		UNION ALL
		SELECT start, category_id, user_id, false FROM vs
	) a
	GROUP BY GROUPING SETS ((start, category_id), (start))
	HAVING GROUPING(category_id) = 1 OR category_id IN (SELECT category_id FROM sec)
),
voiced AS (
	SELECT start, category_id, round(sum(secs))::bigint AS voice_seconds, count(DISTINCT user_id) AS voice_people
	FROM (
		SELECT vs.start, s.category_id, vs.user_id, vs.secs FROM vs JOIN sec s ON CASE
			WHEN cardinality(s.role_ids) > 0 THEN vs.role_ids && s.role_ids
			ELSE vs.category_id = s.category_id END
		UNION ALL
		SELECT start, '', user_id, secs FROM vs
	) v
	GROUP BY start, category_id
)
INSERT INTO metric_weeks (week, category_id, messages, voice_seconds, voice_people, active)
SELECT (start AT TIME ZONE 'UTC')::date, category_id,
	COALESCE(c.messages, 0), COALESCE(v.voice_seconds, 0), COALESCE(v.voice_people, 0), COALESCE(c.active, 0)
FROM counted c FULL JOIN voiced v USING (start, category_id)`

// RollUpMetricWeeks recomputes the weekly totals from the raw rows for the
// weeks in [from, to), both Mondays, replacing what those weeks held.
func (p *Postgres) RollUpMetricWeeks(ctx context.Context, from, to time.Time, sections []metrics.Section) error {
	var categories, roleCategories, roles []string
	for _, s := range sections {
		categories = append(categories, s.CategoryID)
		for _, r := range s.RoleIDs {
			roleCategories, roles = append(roleCategories, s.CategoryID), append(roles, r)
		}
	}
	err := p.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM metric_weeks
			WHERE week >= ($1::timestamptz AT TIME ZONE 'UTC')::date AND week < ($2::timestamptz AT TIME ZONE 'UTC')::date`,
			from, to); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, rollUpWeeks, from, to, categories, roleCategories, roles)
		return err
	})
	if err != nil {
		return fmt.Errorf("roll up metric weeks from %v to %v: %w", from, to, err)
	}
	return nil
}

// PruneMetrics deletes the raw rows older than before. An open session and
// the weekly totals stay.
func (p *Postgres) PruneMetrics(ctx context.Context, before time.Time) error {
	if _, err := p.db.ExecContext(ctx, `
		WITH m AS (DELETE FROM metric_messages WHERE sent_at < $1),
		v AS (DELETE FROM metric_voice_sessions WHERE ended_at < $1)
		DELETE FROM metric_member_events WHERE at < $1`, before); err != nil {
		return fmt.Errorf("prune metrics before %v: %w", before, err)
	}
	return nil
}

// MetricWeeks returns the weekly totals of the weeks in [from, to), server
// rows included.
func (p *Postgres) MetricWeeks(ctx context.Context, from, to time.Time) ([]metrics.WeekTotal, error) {
	weeks, err := queryAll(ctx, p.db, func(row scanner) (metrics.WeekTotal, error) {
		var w metrics.WeekTotal
		err := row.Scan(&w.Week, &w.CategoryID, &w.Messages, &w.VoiceSeconds, &w.VoicePeople, &w.Active)
		return w, err
	}, `
		SELECT week, category_id, messages, voice_seconds, voice_people, active FROM metric_weeks
		WHERE week >= ($1::timestamptz AT TIME ZONE 'UTC')::date AND week < ($2::timestamptz AT TIME ZONE 'UTC')::date
		ORDER BY week, category_id`, from, to)
	if err != nil {
		return nil, fmt.Errorf("list metric weeks from %v to %v: %w", from, to, err)
	}
	return weeks, nil
}

// ActiveDays returns each member's UTC days with a message or voice time,
// from from's day through now's day. An open session runs to now.
func (p *Postgres) ActiveDays(ctx context.Context, from, now time.Time) ([]metrics.ActiveDay, error) {
	u := from.UTC()
	fromDay := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	days, err := queryAll(ctx, p.db, func(row scanner) (metrics.ActiveDay, error) {
		var d metrics.ActiveDay
		err := row.Scan(&d.UserID, &d.Day)
		return d, err
	}, `
		SELECT user_id, (sent_at AT TIME ZONE 'UTC')::date FROM metric_messages
		WHERE sent_at >= $1 AND sent_at <= $2
		UNION
		SELECT v.user_id, d::date FROM metric_voice_sessions v,
			generate_series(
				date_trunc('day', GREATEST(v.started_at, $1) AT TIME ZONE 'UTC'),
				LEAST(COALESCE(v.ended_at, $2), $2) AT TIME ZONE 'UTC',
				interval '1 day') AS d
		WHERE v.started_at <= $2 AND COALESCE(v.ended_at, $2) >= $1
		ORDER BY 2, 1`, fromDay, now)
	if err != nil {
		return nil, fmt.Errorf("list active days from %v: %w", from, err)
	}
	return days, nil
}

// MemberEvents returns the joins and leaves at or after from, oldest first.
func (p *Postgres) MemberEvents(ctx context.Context, from time.Time) ([]metrics.MemberEvent, error) {
	events, err := queryAll(ctx, p.db, func(row scanner) (metrics.MemberEvent, error) {
		var e metrics.MemberEvent
		err := row.Scan(&e.UserID, &e.Kind, &e.At)
		return e, err
	}, `SELECT user_id, kind, at FROM metric_member_events WHERE at >= $1 ORDER BY at, user_id, kind`, from)
	if err != nil {
		return nil, fmt.Errorf("list member events from %v: %w", from, err)
	}
	return events, nil
}

// VoiceSpans returns the voice sessions overlapping [from, to), clipped to
// it. An open session ends at to.
func (p *Postgres) VoiceSpans(ctx context.Context, from, to time.Time) ([]metrics.Span, error) {
	spans, err := queryAll(ctx, p.db, func(row scanner) (metrics.Span, error) {
		var s metrics.Span
		err := row.Scan(&s.Start, &s.End)
		return s, err
	}, `
		SELECT GREATEST(started_at, $1), LEAST(COALESCE(ended_at, $2), $2) FROM metric_voice_sessions
		WHERE started_at < $2 AND COALESCE(ended_at, $2) > $1
		ORDER BY 1, 2`, from, to)
	if err != nil {
		return nil, fmt.Errorf("list voice spans from %v to %v: %w", from, to, err)
	}
	return spans, nil
}

// FirstReplies returns each join in [joinedFrom, joinedTo) with the member's
// first message at or after it and the first later message by someone else
// that replies to or mentions them.
func (p *Postgres) FirstReplies(ctx context.Context, joinedFrom, joinedTo time.Time) ([]metrics.FirstReply, error) {
	replies, err := queryAll(ctx, p.db, func(row scanner) (metrics.FirstReply, error) {
		var (
			r              metrics.FirstReply
			first, replied sql.NullTime
		)
		err := row.Scan(&r.UserID, &r.JoinedAt, &first, &replied)
		r.FirstMessageAt, r.RepliedAt = first.Time, replied.Time
		return r, err
	}, `
		SELECT j.user_id, j.at, f.sent_at, r.sent_at
		FROM metric_member_events j
		CROSS JOIN LATERAL (
			SELECT min(m.sent_at) AS sent_at FROM metric_messages m
			WHERE m.user_id = j.user_id AND m.sent_at >= j.at
		) f
		CROSS JOIN LATERAL (
			SELECT min(m.sent_at) AS sent_at FROM metric_messages m
			WHERE m.user_id <> j.user_id AND m.sent_at > f.sent_at AND m.addressed_ids @> ARRAY[j.user_id]
		) r
		WHERE j.kind = 'join' AND j.at >= $1 AND j.at < $2
		ORDER BY j.at, j.user_id`, joinedFrom, joinedTo)
	if err != nil {
		return nil, fmt.Errorf("list first replies of joins from %v: %w", joinedFrom, err)
	}
	return replies, nil
}
