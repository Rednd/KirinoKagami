CREATE TABLE IF NOT EXISTS sets (
    id              INT UNSIGNED     NOT NULL PRIMARY KEY,
    ranked_status   TINYINT          NOT NULL,
    approved_date   DATETIME         NULL,
    last_update     DATETIME         NOT NULL,
    last_checked    DATETIME         NULL,
    next_check      DATETIME         NOT NULL,
    artist          VARCHAR(512)     NOT NULL DEFAULT '',
    artist_unicode  VARCHAR(512)     NOT NULL DEFAULT '',
    title           VARCHAR(512)     NOT NULL DEFAULT '',
    title_unicode   VARCHAR(512)     NOT NULL DEFAULT '',
    creator         VARCHAR(255)     NOT NULL DEFAULT '',
    creator_id      INT UNSIGNED     NOT NULL DEFAULT 0,
    source          TEXT             NOT NULL,
    tags            TEXT             NOT NULL,
    has_video       BOOLEAN          NOT NULL DEFAULT FALSE,
    has_storyboard  BOOLEAN          NOT NULL DEFAULT FALSE,
    genre           TINYINT UNSIGNED NULL,
    language        TINYINT UNSIGNED NULL,
    favourites      INT UNSIGNED     NOT NULL DEFAULT 0,
    modes           TINYINT UNSIGNED NOT NULL DEFAULT 0,
    updated_at      TIMESTAMP        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_sets_next_check (next_check),
    INDEX idx_sets_last_update (last_update)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS beatmaps (
    id                 INT UNSIGNED NOT NULL PRIMARY KEY,
    parent_set_id      INT UNSIGNED NOT NULL,
    diff_name          VARCHAR(512) NOT NULL DEFAULT '',
    file_md5           CHAR(32)     NOT NULL DEFAULT '',
    mode               TINYINT      NOT NULL,
    bpm                DOUBLE       NOT NULL DEFAULT 0,
    ar                 FLOAT        NOT NULL DEFAULT 0,
    od                 FLOAT        NOT NULL DEFAULT 0,
    cs                 FLOAT        NOT NULL DEFAULT 0,
    hp                 FLOAT        NOT NULL DEFAULT 0,
    total_length       INT UNSIGNED NOT NULL DEFAULT 0,
    hit_length         INT UNSIGNED NOT NULL DEFAULT 0,
    playcount          INT UNSIGNED NOT NULL DEFAULT 0,
    passcount          INT UNSIGNED NOT NULL DEFAULT 0,
    max_combo          INT UNSIGNED NOT NULL DEFAULT 0,
    difficulty_rating  DOUBLE       NOT NULL DEFAULT 0,
    INDEX idx_beatmaps_parent (parent_set_id),
    INDEX idx_beatmaps_md5 (file_md5)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS kv (
    k VARCHAR(128) NOT NULL PRIMARY KEY,
    v TEXT         NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
