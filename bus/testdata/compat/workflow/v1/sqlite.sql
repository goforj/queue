CREATE TABLE bus_chains (
    chain_id TEXT PRIMARY KEY,
    dispatch_id TEXT NOT NULL,
    queue_name TEXT NOT NULL,
    nodes_json BLOB NOT NULL,
    next_index INTEGER NOT NULL,
    completed INTEGER NOT NULL,
    failed INTEGER NOT NULL,
    failure TEXT NOT NULL,
    created_at_ms BIGINT NOT NULL,
    updated_at_ms BIGINT NOT NULL
);

CREATE TABLE bus_chain_completed_nodes (
    chain_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    created_at_ms BIGINT NOT NULL,
    PRIMARY KEY (chain_id, node_id)
);

CREATE TABLE bus_batches (
    batch_id TEXT PRIMARY KEY,
    dispatch_id TEXT NOT NULL,
    name TEXT NOT NULL,
    queue_name TEXT NOT NULL,
    allow_failed INTEGER NOT NULL,
    total_jobs INTEGER NOT NULL,
    pending_jobs INTEGER NOT NULL,
    processed_jobs INTEGER NOT NULL,
    failed_jobs INTEGER NOT NULL,
    cancelled INTEGER NOT NULL,
    completed INTEGER NOT NULL,
    created_at_ms BIGINT NOT NULL,
    updated_at_ms BIGINT NOT NULL
);

CREATE TABLE bus_batch_jobs (
    batch_id TEXT NOT NULL,
    job_id TEXT NOT NULL,
    started INTEGER NOT NULL,
    done INTEGER NOT NULL,
    failed INTEGER NOT NULL,
    PRIMARY KEY (batch_id, job_id)
);

CREATE TABLE bus_callback_invocations (
    callback_key TEXT PRIMARY KEY,
    created_at_ms BIGINT NOT NULL
);

INSERT INTO bus_chains
    (chain_id, dispatch_id, queue_name, nodes_json, next_index, completed, failed, failure, created_at_ms, updated_at_ms)
VALUES
    ('compat-chain-mutate', 'compat-dispatch-mutate', 'critical', '[{"NodeID":"legacy-node-1","Job":{"type":"reports:build","payload":"eyJpZCI6MX0=","options":{"Queue":"critical","Delay":2000000000,"Timeout":15000000000,"Retry":4,"Backoff":500000000,"UniqueFor":30000000000}}},{"NodeID":"legacy-node-2","Job":{"type":"reports:notify","payload":"bnVsbA==","options":{"Queue":"critical","Delay":0,"Timeout":0,"Retry":0,"Backoff":0,"UniqueFor":0}}}]', 0, 0, 0, '', 1704067200123, 1704067201123),
    ('compat-chain-active-old', 'compat-dispatch-active-old', 'default', '[{"NodeID":"legacy-node-1","Job":{"type":"reports:build","payload":"eyJpZCI6MX0=","options":{"Queue":"critical","Delay":2000000000,"Timeout":15000000000,"Retry":4,"Backoff":500000000,"UniqueFor":30000000000}}},{"NodeID":"legacy-node-2","Job":{"type":"reports:notify","payload":"bnVsbA==","options":{"Queue":"critical","Delay":0,"Timeout":0,"Retry":0,"Backoff":0,"UniqueFor":0}}}]', 1, 0, 0, '', 1704067200123, 1704067205000),
    ('compat-chain-completed-old', 'compat-dispatch-completed-old', 'default', '[{"NodeID":"legacy-node-1","Job":{"type":"reports:build","payload":"eyJpZCI6MX0=","options":{"Queue":"critical","Delay":2000000000,"Timeout":15000000000,"Retry":4,"Backoff":500000000,"UniqueFor":30000000000}}},{"NodeID":"legacy-node-2","Job":{"type":"reports:notify","payload":"bnVsbA==","options":{"Queue":"critical","Delay":0,"Timeout":0,"Retry":0,"Backoff":0,"UniqueFor":0}}}]', 2, 1, 0, '', 1704067200123, 1704067205000),
    ('compat-chain-failed-old', 'compat-dispatch-failed-old', 'default', '[{"NodeID":"legacy-node-1","Job":{"type":"reports:build","payload":"eyJpZCI6MX0=","options":{"Queue":"critical","Delay":2000000000,"Timeout":15000000000,"Retry":4,"Backoff":500000000,"UniqueFor":30000000000}}},{"NodeID":"legacy-node-2","Job":{"type":"reports:notify","payload":"bnVsbA==","options":{"Queue":"critical","Delay":0,"Timeout":0,"Retry":0,"Backoff":0,"UniqueFor":0}}}]', 1, 0, 1, 'legacy failure', 1704067200123, 1704067205000),
    ('compat-chain-completed-recent', 'compat-dispatch-completed-recent', 'critical', '[{"NodeID":"legacy-node-1","Job":{"type":"reports:build","payload":"eyJpZCI6MX0=","options":{"Queue":"critical","Delay":2000000000,"Timeout":15000000000,"Retry":4,"Backoff":500000000,"UniqueFor":30000000000}}},{"NodeID":"legacy-node-2","Job":{"type":"reports:notify","payload":"bnVsbA==","options":{"Queue":"critical","Delay":0,"Timeout":0,"Retry":0,"Backoff":0,"UniqueFor":0}}}]', 2, 1, 0, '', 1706000000123, 1706000001123);

INSERT INTO bus_chain_completed_nodes (chain_id, node_id, created_at_ms)
VALUES
    ('compat-chain-active-old', 'legacy-node-1', 1704067204000),
    ('compat-chain-completed-old', 'legacy-node-1', 1704067203000),
    ('compat-chain-completed-old', 'legacy-node-2', 1704067204000),
    ('compat-chain-failed-old', 'legacy-node-1', 1704067203000),
    ('compat-chain-completed-recent', 'legacy-node-1', 1706000000123),
    ('compat-chain-completed-recent', 'legacy-node-2', 1706000001123);

INSERT INTO bus_batches
    (batch_id, dispatch_id, name, queue_name, allow_failed, total_jobs, pending_jobs, processed_jobs, failed_jobs, cancelled, completed, created_at_ms, updated_at_ms)
VALUES
    ('compat-batch-mutate', 'compat-batch-dispatch-mutate', 'legacy mutable batch', 'bulk', 1, 2, 2, 0, 0, 0, 0, 1704067200123, 1704067201123),
    ('compat-batch-active-old', 'compat-batch-dispatch-active-old', 'legacy active batch', 'default', 0, 1, 1, 0, 0, 0, 0, 1704067200123, 1704067205000),
    ('compat-batch-terminal-old', 'compat-batch-dispatch-terminal-old', 'legacy terminal batch', 'bulk', 1, 2, 0, 2, 1, 0, 1, 1704067200123, 1704067205000),
    ('compat-batch-terminal-recent', 'compat-batch-dispatch-terminal-recent', 'recent terminal batch', 'critical', 0, 1, 0, 1, 0, 0, 1, 1706000000123, 1706000001123);

INSERT INTO bus_batch_jobs (batch_id, job_id, started, done, failed)
VALUES
    ('compat-batch-mutate', 'legacy-batch-job-1', 0, 0, 0),
    ('compat-batch-mutate', 'legacy-batch-job-2', 0, 0, 0),
    ('compat-batch-active-old', 'legacy-active-job-1', 0, 0, 0),
    ('compat-batch-terminal-old', 'legacy-terminal-job-1', 1, 1, 0),
    ('compat-batch-terminal-old', 'legacy-terminal-job-2', 1, 1, 1),
    ('compat-batch-terminal-recent', 'legacy-recent-job-1', 1, 1, 0);

INSERT INTO bus_callback_invocations (callback_key, created_at_ms)
VALUES
    ('chain_finally:compat-chain-completed-old', 1704067205000),
    ('chain_finally:compat-chain-completed-recent', 1706000001123);
