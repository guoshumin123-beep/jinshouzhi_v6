-- internal/subaccount：火币子账户开户状态机、加密 API Key、母子/现货-合约划转。

CREATE TABLE subaccounts (
    user_id          BIGINT       NOT NULL,
    user_name        VARCHAR(20)  NOT NULL COMMENT '火币子用户名（全平台唯一，6-20 位字母数字，字母开头）',
    name_attempt     INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '用户名被他人占用时的重试序号',
    sub_uid          BIGINT       NULL COMMENT '火币子用户 UID（CREATED 之后非空）',
    sub_account_type VARCHAR(16)  NOT NULL,
    state            VARCHAR(16)  NOT NULL COMMENT 'PROVISIONING / READY / LOCKED / FAILED',
    step             VARCHAR(24)  NOT NULL COMMENT '已完成的最后一步',
    attempts         INT UNSIGNED NOT NULL DEFAULT 0,
    last_error       VARCHAR(512) NULL,
    lease_owner      VARCHAR(64)  NULL COMMENT '正在执行开户的进程（租约，防止多实例并发推进）',
    lease_until      DATETIME(6)  NULL,
    created_at       DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at       DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (user_id),
    UNIQUE KEY uk_user_name (user_name),
    UNIQUE KEY uk_sub_uid (sub_uid),
    KEY idx_state (state, lease_until),
    CONSTRAINT chk_subaccount_state CHECK (state IN ('PROVISIONING', 'READY', 'LOCKED', 'FAILED'))
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin COMMENT = '火币子账户';

-- Secret 仅以 AES-256-GCM 密文存储（internal/vault），AAD = subaccount:<user_id>:<access_key>。
CREATE TABLE subaccount_api_keys (
    id                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id           BIGINT          NOT NULL,
    sub_uid           BIGINT          NOT NULL,
    access_key        VARCHAR(128)    NOT NULL,
    secret_key_id     VARCHAR(32)     NOT NULL COMMENT 'vault 密钥 id',
    secret_nonce      VARBINARY(12)   NOT NULL,
    secret_ciphertext VARBINARY(512)  NOT NULL,
    permission        VARCHAR(64)     NOT NULL,
    ip_addresses      VARCHAR(1024)   NOT NULL,
    note              VARCHAR(255)    NOT NULL,
    status            VARCHAR(12)     NOT NULL COMMENT 'ACTIVE / REVOKED',
    active_slot       TINYINT GENERATED ALWAYS AS (IF(status = 'ACTIVE', 1, NULL)) STORED,
    created_at        DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    revoked_at        DATETIME(6)     NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_access_key (access_key),
    UNIQUE KEY uk_one_active (user_id, active_slot),
    CONSTRAINT chk_api_key_status CHECK (status IN ('ACTIVE', 'REVOKED'))
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin COMMENT = '子账户 API Key（加密）';

-- 一次划转请求由多段（leg）组成，每段一行。
--   TO_SUB  : 1) MASTER_TO_SUB_SPOT  2) SUB_SPOT_TO_SWAP
--   FROM_SUB: 1) SUB_SWAP_TO_SPOT    2) SUB_SPOT_TO_MASTER
-- 母子划转携带 client-order-id（火币幂等）；现货-合约划转接口无幂等键，依靠本地状态 + 余额核验，结果未知时绝不盲目重发。
CREATE TABLE subaccount_transfers (
    id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    request_id       VARCHAR(64)     NOT NULL COMMENT '调用方幂等键',
    user_id          BIGINT          NOT NULL,
    sub_uid          BIGINT          NOT NULL,
    direction        VARCHAR(8)      NOT NULL COMMENT 'TO_SUB / FROM_SUB',
    leg              TINYINT         NOT NULL,
    kind             VARCHAR(24)     NOT NULL,
    currency         VARCHAR(16)     NOT NULL,
    amount           DECIMAL(36, 18) NOT NULL,
    margin_account   VARCHAR(32)     NOT NULL COMMENT 'USDT（全仓）或小写合约代码（逐仓）',
    client_order_id  CHAR(10)        NULL COMMENT '仅母子划转使用',
    status           VARCHAR(12)     NOT NULL COMMENT 'PENDING / IN_FLIGHT / SUCCEEDED / FAILED / UNKNOWN / MANUAL',
    spot_before      DECIMAL(36, 18) NULL COMMENT '发起前子账户现货余额快照（用于结果未知时核验）',
    htx_transfer_id  BIGINT          NULL,
    attempts         INT UNSIGNED    NOT NULL DEFAULT 0,
    last_error       VARCHAR(512)    NULL,
    active_slot      TINYINT GENERATED ALWAYS AS (IF(status IN ('PENDING', 'IN_FLIGHT', 'UNKNOWN'), 1, NULL)) STORED,
    created_at       DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at       DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uk_request_leg (user_id, request_id, leg),
    UNIQUE KEY uk_client_order_id (client_order_id),
    -- 每个用户同一时刻最多一段在途划转：子账户现货余额只被本系统划转改变，串行化后余额核验才可靠。
    -- 第 2 段在第 1 段成功后才创建。
    UNIQUE KEY uk_user_active (user_id, active_slot),
    KEY idx_status (status, updated_at),
    CONSTRAINT chk_transfer_status CHECK (status IN ('PENDING', 'IN_FLIGHT', 'SUCCEEDED', 'FAILED', 'UNKNOWN', 'MANUAL'))
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin COMMENT = '子账户划转';
