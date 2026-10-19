-- internal/dealer 账本表。仅 dealer 使用的 DB 账号拥有 accounts 的 UPDATE 权限。
-- 金额统一 DECIMAL(36,18)；时间统一 DATETIME(6)。

CREATE TABLE IF NOT EXISTS accounts (
    id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    owner_id     BIGINT          NOT NULL COMMENT '用户ID；系统账户为 0',
    account_type VARCHAR(32)     NOT NULL COMMENT 'SPOT/SWAP/HTX_CUSTODY/SYNTHETIC_POOL/FEE_INCOME/...',
    currency     VARCHAR(16)     NOT NULL,
    available    DECIMAL(36, 18) NOT NULL DEFAULT 0,
    frozen       DECIMAL(36, 18) NOT NULL DEFAULT 0,
    version      BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '每次变动 +1',
    created_at   DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at   DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uk_owner_type_ccy (owner_id, account_type, currency),
    -- 纵深防御（MySQL >= 8.0.16 生效）：用户账户余额不得为负，系统账户允许为负
    CONSTRAINT chk_accounts_non_negative CHECK (
        account_type IN ('SYNTHETIC_POOL', 'FEE_INCOME', 'DEPOSIT_SUSPENSE', 'WITHDRAW_SUSPENSE', 'HTX_PNL')
        OR (available >= 0 AND frozen >= 0)
    )
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin COMMENT = '资产余额快照';

CREATE TABLE IF NOT EXISTS ledger_txns (
    id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    biz_type     VARCHAR(32)     NOT NULL,
    biz_id       VARCHAR(128)    NOT NULL,
    payload_hash CHAR(64)        NOT NULL COMMENT 'Postings 的 SHA-256，用于识别同幂等键不同内容',
    memo         JSON            NULL,
    created_at   DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uk_biz (biz_type, biz_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin COMMENT = '账本事务（幂等）';

CREATE TABLE IF NOT EXISTS ledger_entries (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    txn_id          BIGINT UNSIGNED NOT NULL,
    seq             INT UNSIGNED    NOT NULL COMMENT '事务内序号',
    account_id      BIGINT UNSIGNED NOT NULL,
    op              VARCHAR(16)     NOT NULL,
    amount          DECIMAL(36, 18) NOT NULL,
    available_delta DECIMAL(36, 18) NOT NULL,
    frozen_delta    DECIMAL(36, 18) NOT NULL,
    available_after DECIMAL(36, 18) NOT NULL,
    frozen_after    DECIMAL(36, 18) NOT NULL,
    created_at      DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uk_txn_seq (txn_id, seq),
    KEY idx_account (account_id, id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin COMMENT = '复式分录（只追加）';
