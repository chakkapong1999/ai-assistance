-- Initial schema. Bitbucket UUIDs are the natural keys of everything that
-- comes from Bitbucket, so sync can upsert with ON CONFLICT (bb_uuid).
-- The job queue is NOT here: River creates its own tables in M3.

CREATE TABLE workspaces (
    id         bigserial PRIMARY KEY,
    bb_uuid    text NOT NULL UNIQUE,
    slug       text NOT NULL,
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE projects (
    id           bigserial PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    bb_uuid      text NOT NULL UNIQUE,
    key          text NOT NULL,
    name         text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX projects_workspace_idx ON projects (workspace_id);

CREATE TABLE repositories (
    id             bigserial PRIMARY KEY,
    project_id     bigint NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    bb_uuid        text NOT NULL UNIQUE,
    slug           text NOT NULL,
    name           text NOT NULL,
    main_language  text,
    default_branch text,
    -- Off by default: a repo is only reviewed after someone enables it,
    -- because code is sent to an LLM.
    review_enabled boolean NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX repositories_project_idx ON repositories (project_id);

CREATE TABLE users (
    id           bigserial PRIMARY KEY,
    -- Both are NULL for authors that only exist as a raw "Name <email>" string.
    bb_account_id text UNIQUE,
    bb_uuid       text UNIQUE,
    display_name  text NOT NULL,
    nickname      text,
    avatar_url    text,
    email         text,
    -- Not available from Bitbucket Cloud; filled by an admin or an HR/AD sync.
    job_title     text,
    department    text,
    synced_at     timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE user_emails (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email   text NOT NULL,
    source  text NOT NULL DEFAULT 'commit',
    PRIMARY KEY (user_id, email)
);
-- One email address belongs to exactly one user.
CREATE UNIQUE INDEX user_emails_email_key ON user_emails (lower(email));

CREATE TABLE workspace_members (
    workspace_id bigint NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id      bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role         text NOT NULL,
    PRIMARY KEY (workspace_id, user_id)
);

CREATE TABLE repo_permissions (
    repo_id    bigint NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    user_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    permission text NOT NULL CHECK (permission IN ('admin', 'write', 'read')),
    PRIMARY KEY (repo_id, user_id)
);
CREATE INDEX repo_permissions_user_idx ON repo_permissions (user_id);

CREATE TABLE commits (
    id               bigserial PRIMARY KEY,
    repo_id          bigint NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    hash             text NOT NULL,
    author_user_id   bigint REFERENCES users(id) ON DELETE SET NULL,
    author_raw       text,
    message          text NOT NULL DEFAULT '',
    branch           text,
    committed_at     timestamptz NOT NULL,
    additions        integer,
    deletions        integer,
    files_changed    integer,
    is_merge         boolean NOT NULL DEFAULT false,
    review_status    text NOT NULL DEFAULT 'pending'
        CHECK (review_status IN ('pending', 'running', 'done', 'skipped', 'failed')),
    review_skip_reason text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (repo_id, hash)
);
CREATE INDEX commits_repo_time_idx   ON commits (repo_id, committed_at DESC);
CREATE INDEX commits_author_time_idx ON commits (author_user_id, committed_at DESC);
CREATE INDEX commits_status_idx      ON commits (review_status) WHERE review_status IN ('pending', 'running');

CREATE TABLE pull_requests (
    id             bigserial PRIMARY KEY,
    repo_id        bigint NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    bb_pr_id       integer NOT NULL,
    title          text NOT NULL,
    author_user_id bigint REFERENCES users(id) ON DELETE SET NULL,
    source_branch  text,
    dest_branch    text,
    state          text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (repo_id, bb_pr_id)
);

CREATE TABLE webhook_events (
    id           bigserial PRIMARY KEY,
    -- X-Request-UUID from Bitbucket; Bitbucket re-sends the same UUID on retry.
    request_uuid text NOT NULL UNIQUE,
    event_key    text NOT NULL,
    payload      jsonb NOT NULL,
    received_at  timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz
);
CREATE INDEX webhook_events_unprocessed_idx ON webhook_events (received_at) WHERE processed_at IS NULL;

CREATE TABLE reviews (
    id             bigserial PRIMARY KEY,
    commit_id      bigint NOT NULL REFERENCES commits(id) ON DELETE CASCADE,
    -- 'mock' marks results that did not come from a real model.
    model          text NOT NULL,
    prompt_version text NOT NULL,
    score          integer CHECK (score BETWEEN 0 AND 100),
    summary        text NOT NULL DEFAULT '',
    tokens_in      integer,
    tokens_out     integer,
    cost_usd       numeric(10, 4),
    duration_ms    integer,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reviews_commit_idx ON reviews (commit_id, created_at DESC);

CREATE TABLE review_findings (
    id          bigserial PRIMARY KEY,
    review_id   bigint NOT NULL REFERENCES reviews(id) ON DELETE CASCADE,
    file_path   text NOT NULL,
    line_start  integer NOT NULL,
    line_end    integer NOT NULL,
    severity    text NOT NULL CHECK (severity IN ('critical', 'major', 'minor', 'info')),
    category    text NOT NULL,
    title       text NOT NULL,
    explanation text NOT NULL DEFAULT '',
    CHECK (line_end >= line_start)
);
CREATE INDEX review_findings_review_idx ON review_findings (review_id, severity);

CREATE TABLE code_suggestions (
    id                bigserial PRIMARY KEY,
    finding_id        bigint NOT NULL REFERENCES review_findings(id) ON DELETE CASCADE,
    original_snippet  text NOT NULL,
    suggested_snippet text NOT NULL,
    unified_diff      text NOT NULL,
    status            text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'accepted', 'dismissed'))
);
CREATE INDEX code_suggestions_finding_idx ON code_suggestions (finding_id);

CREATE TABLE review_feedback (
    id         bigserial PRIMARY KEY,
    finding_id bigint NOT NULL REFERENCES review_findings(id) ON DELETE CASCADE,
    user_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    verdict    text NOT NULL CHECK (verdict IN ('accepted', 'dismissed', 'false_positive')),
    comment    text,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX review_feedback_finding_idx ON review_feedback (finding_id);

CREATE TABLE review_rules (
    id         bigserial PRIMARY KEY,
    scope      text NOT NULL CHECK (scope IN ('global', 'project', 'repo')),
    -- project id or repo id depending on scope; NULL for global.
    scope_id   bigint,
    language   text,
    rule_text  text NOT NULL,
    enabled    boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((scope = 'global') = (scope_id IS NULL))
);
