-- +goose Up
CREATE TABLE users (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subject      text NOT NULL UNIQUE,
    email        text NOT NULL DEFAULT '',
    name         text NOT NULL DEFAULT '',
    github_login text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_github_login_key ON users (lower(github_login));

-- Repository and issue keys are lowercase "owner/name".
CREATE TABLE repos (
    full_name      text PRIMARY KEY,
    node_id        text NOT NULL DEFAULT '',
    default_branch text NOT NULL DEFAULT '',
    archived       boolean NOT NULL DEFAULT false,
    project        jsonb,
    status_map     jsonb,
    synced_at      timestamptz
);

CREATE TABLE issues (
    repo                text NOT NULL,
    number              integer NOT NULL CHECK (number > 0),
    node_id             text NOT NULL DEFAULT '',
    title               text NOT NULL DEFAULT '',
    url                 text NOT NULL DEFAULT '',
    gh_state            text NOT NULL DEFAULT 'open',
    assignees           text[] NOT NULL DEFAULT '{}',
    labels              text[] NOT NULL DEFAULT '{}',
    placeholder         boolean NOT NULL DEFAULT true,
    state               text NOT NULL DEFAULT 'todo',
    branch              text NOT NULL DEFAULT '',
    started_at          timestamptz,
    pr_opened_at        timestamptz,
    pr_number           integer,
    closed_at           timestamptz,
    snapshot_usd_micros bigint,
    snapshot_tokens     jsonb,
    snapshot_at         timestamptz,
    gh_updated_at       timestamptz,
    updated_at          timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (repo, number)
);
CREATE INDEX issues_assignees_idx ON issues USING gin (assignees);
CREATE INDEX issues_state_idx ON issues (state);
CREATE INDEX issues_placeholder_idx ON issues (repo, number) WHERE placeholder;

CREATE TABLE usage_events (
    id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    dedup_key                text NOT NULL UNIQUE,
    user_id                  uuid NOT NULL REFERENCES users (id),
    harness                  text NOT NULL,
    external_id              text NOT NULL,
    session_id               text NOT NULL DEFAULT '',
    provider                 text NOT NULL DEFAULT '',
    model                    text NOT NULL,
    machine_id               text NOT NULL DEFAULT '',
    input_tokens             bigint NOT NULL DEFAULT 0,
    output_tokens            bigint NOT NULL DEFAULT 0,
    reasoning_tokens         bigint NOT NULL DEFAULT 0,
    cache_read_tokens        bigint NOT NULL DEFAULT 0,
    cache_write_tokens       bigint NOT NULL DEFAULT 0,
    occurred_at              timestamptz NOT NULL,
    received_at              timestamptz NOT NULL DEFAULT now(),
    bucket                   text NOT NULL CHECK (bucket IN ('issue', 'repo', 'external')),
    source                   text NOT NULL,
    repo                     text,
    branch                   text NOT NULL DEFAULT '',
    issue_number             integer,
    reported_cost_usd_micros bigint NOT NULL DEFAULT 0,
    cost_usd_micros          bigint NOT NULL DEFAULT 0,
    price_id                 bigint,
    priced                   boolean NOT NULL DEFAULT false,
    CHECK ((bucket = 'issue') = (issue_number IS NOT NULL))
);
CREATE INDEX usage_events_time_idx ON usage_events (occurred_at DESC, id DESC);
CREATE INDEX usage_events_issue_idx ON usage_events (repo, issue_number) WHERE issue_number IS NOT NULL;
CREATE INDEX usage_events_user_time_idx ON usage_events (user_id, occurred_at DESC);
CREATE INDEX usage_events_inbox_idx ON usage_events (user_id, session_id) WHERE bucket <> 'issue';
CREATE INDEX usage_events_session_idx ON usage_events (session_id);

CREATE TABLE reassignments (
    id               bigserial PRIMARY KEY,
    event_id         uuid NOT NULL REFERENCES usage_events (id) ON DELETE CASCADE,
    actor_id         uuid NOT NULL REFERENCES users (id),
    from_attribution jsonb NOT NULL,
    to_attribution   jsonb NOT NULL,
    reason           text NOT NULL DEFAULT '',
    at               timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reassignments_event_idx ON reassignments (event_id);

CREATE TABLE prices (
    id             bigserial PRIMARY KEY,
    provider       text NOT NULL DEFAULT '',
    model          text NOT NULL,
    input          double precision NOT NULL DEFAULT 0,
    output         double precision NOT NULL DEFAULT 0,
    cache_read     double precision NOT NULL DEFAULT 0,
    cache_write    double precision NOT NULL DEFAULT 0,
    effective_from timestamptz NOT NULL,
    source         text NOT NULL CHECK (source IN ('models.dev', 'manual')),
    created_by     uuid REFERENCES users (id),
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX prices_model_idx ON prices (lower(model), effective_from DESC);

CREATE TABLE fx_rates (
    day         date PRIMARY KEY,
    usd_per_eur double precision NOT NULL CHECK (usd_per_eur > 0)
);

CREATE TABLE meta (
    key        text PRIMARY KEY,
    value      text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE meta;
DROP TABLE fx_rates;
DROP TABLE prices;
DROP TABLE reassignments;
DROP TABLE usage_events;
DROP TABLE issues;
DROP TABLE repos;
DROP TABLE users;
