ALTER TABLE model_calls ADD COLUMN iteration INTEGER NOT NULL DEFAULT 0;
ALTER TABLE model_calls ADD COLUMN attempt INTEGER NOT NULL DEFAULT 1;
ALTER TABLE model_calls ADD COLUMN status TEXT NOT NULL DEFAULT 'started';

ALTER TABLE tool_calls ADD COLUMN iteration INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tool_calls ADD COLUMN call_index INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tool_calls ADD COLUMN arguments_fragment TEXT;

UPDATE model_calls SET iteration = seq WHERE iteration = 0;
UPDATE tool_calls SET iteration = seq WHERE iteration = 0;

CREATE UNIQUE INDEX IF NOT EXISTS ux_model_calls_run_iteration_attempt
    ON model_calls(run_id, iteration, attempt);
CREATE UNIQUE INDEX IF NOT EXISTS ux_tool_calls_run_iteration_index
    ON tool_calls(run_id, iteration, call_index);
