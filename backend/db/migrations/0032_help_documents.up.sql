SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE help_documents (
    id VARCHAR(80) PRIMARY KEY,
    version BIGINT NOT NULL CHECK (version > 0),
    draft_json TEXT NOT NULL,
    published_json TEXT NOT NULL DEFAULT ''
);
CREATE TABLE help_document_revisions (
    document_id VARCHAR(80) NOT NULL REFERENCES help_documents(id),
    version BIGINT NOT NULL CHECK (version > 0),
    snapshot_json TEXT NOT NULL,
    PRIMARY KEY (document_id, version)
);
