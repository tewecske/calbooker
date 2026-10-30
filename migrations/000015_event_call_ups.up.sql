-- Per-event call-ups and leaving players out (#47).
--
-- called_up: a coach of the event (or staff) called the player up for an event
--            that is not the player's team's event. The row is the player's
--            access to the event: a member with a called_up row sees that
--            event (and a linked parent sees it for the child). A called-up row
--            has status 'nominated' (called up) or 'declined' (the player or
--            parent opted out and may take the place back), so the choice
--            survives the answer. Confirmed attendance on a called-up row
--            counts in the player's statistics.
-- left_out:  a new status. A coach left a player of the event's teams out of
--            the event. It takes no place against the participant limit, the
--            player cannot answer, and it is not a statistics opportunity
--            unless the player is marked as attended anyway.
ALTER TABLE event_participants ADD COLUMN called_up BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE event_participants DROP CONSTRAINT event_participants_status_allowed;
ALTER TABLE event_participants ADD CONSTRAINT event_participants_status_allowed
    CHECK (status IS NULL OR status IN ('signed_up', 'declined', 'nominated', 'left_out'));
ALTER TABLE event_participants ADD CONSTRAINT event_participants_called_up_answer
    CHECK (NOT called_up OR status IN ('nominated', 'declined'));

CREATE INDEX event_participants_called_up_idx ON event_participants (group_id, user_id) WHERE called_up;
