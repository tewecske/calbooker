-- Sign-up and attendance (#9): one row per event and member. A row holds the
-- member's answer (status) and, after the event, the confirmed attendance.
-- Participation statistics (#11) count rows with attended = true.
--
-- status:   signed_up ("I'll be there", open events), declined ("Can't come",
--           or a nominated player who opted out), nominated (picked by a
--           coach), or NULL for a walk-in a coach added after the event
--           without an answer.
-- attended: NULL until a coach confirms attendance, then true or false.
--
-- Rows are keyed by user ID, so a guest's participation survives the guest
-- upgrade. Leaving the group (or guest retention deleting the guest) removes
-- the rows with the membership; deleting the event removes them too, but the
-- event service refuses to delete an event that has rows.
CREATE TABLE event_participants (
    group_id BIGINT NOT NULL,
    event_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    status VARCHAR(16),
    attended BOOLEAN,
    -- Who changed the row last (the member, a parent, a coach, or staff)
    -- and when (Unix seconds).
    updated_by BIGINT,
    updated_at BIGINT NOT NULL,
    PRIMARY KEY (event_id, user_id),
    CONSTRAINT event_participants_status_allowed CHECK (status IS NULL OR status IN ('signed_up', 'declined', 'nominated')),
    -- A row without an answer exists only as a confirmed walk-in.
    CONSTRAINT event_participants_answer_or_attendance CHECK (status IS NOT NULL OR attended IS NOT NULL),
    CONSTRAINT event_participants_event_fk FOREIGN KEY (group_id, event_id)
        REFERENCES events (group_id, id) ON DELETE CASCADE,
    CONSTRAINT event_participants_member_fk FOREIGN KEY (group_id, user_id)
        REFERENCES group_members (group_id, user_id) ON DELETE CASCADE,
    CONSTRAINT event_participants_updated_by_fk FOREIGN KEY (updated_by) REFERENCES users (id) ON DELETE SET NULL
);

CREATE INDEX event_participants_member_idx ON event_participants (group_id, user_id);
CREATE INDEX event_participants_attended_idx ON event_participants (group_id, event_id) WHERE attended;
