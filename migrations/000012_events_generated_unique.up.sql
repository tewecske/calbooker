-- Event generation (#15): a training makes at most one event per week, so
-- generating the same week twice cannot create it twice, even when two
-- requests race. Hand-made events carry neither column and are not affected.
CREATE UNIQUE INDEX events_generated_unique ON events (group_id, plan_training_id, generated_for_week)
    WHERE plan_training_id IS NOT NULL AND generated_for_week IS NOT NULL;
