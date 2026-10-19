-- internal/gateway：路由画像、持仓（路由固化）与订单。

CREATE TABLE IF NOT EXISTS user_route_profiles (
    user_id    BIGINT      NOT NULL,
    route      VARCHAR(16) NOT NULL COMMENT 'STANDARD / SYNTHETIC，仅影响未来新开仓',
    updated_by VARCHAR(64) NOT NULL COMMENT 'Admin 操作人',
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (user_id),
    CONSTRAINT chk_profile_route CHECK (route IN ('STANDARD', 'SYNTHETIC'))
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin COMMENT = '用户合约路由画像';

-- 单向持仓下每个 (user, contract) 同时最多一个 OPEN 持仓。该表行仅用作串行化锁，
-- 保证“判定路由 → 创建持仓”在并发下原子，也与 Admin 修改画像串行。
CREATE TABLE IF NOT EXISTS position_slots (
    user_id       BIGINT      NOT NULL,
    contract_code VARCHAR(32) NOT NULL,
    created_at    DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (user_id, contract_code)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin COMMENT = '持仓槽位锁';

CREATE TABLE IF NOT EXISTS swap_positions (
    id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id       BIGINT          NOT NULL,
    contract_code VARCHAR(32)     NOT NULL,
    route         VARCHAR(16)     NOT NULL COMMENT '开仓时固化，生命周期内不可修改',
    direction     VARCHAR(8)      NOT NULL COMMENT 'LONG / SHORT（单向持仓）',
    volume        DECIMAL(36, 18) NOT NULL DEFAULT 0 COMMENT '张数',
    avg_price     DECIMAL(36, 18) NOT NULL DEFAULT 0,
    margin        DECIMAL(36, 18) NOT NULL DEFAULT 0 COMMENT 'Synthetic：dealer 中冻结的保证金',
    leverage      INT UNSIGNED    NOT NULL,
    realized_pnl  DECIMAL(36, 18) NOT NULL DEFAULT 0,
    status        VARCHAR(8)      NOT NULL COMMENT 'OPEN / CLOSED',
    open_slot     TINYINT GENERATED ALWAYS AS (IF(status = 'OPEN', 1, NULL)) STORED,
    opened_at     DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    closed_at     DATETIME(6)     NULL,
    updated_at    DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    -- NULL 不参与唯一性：CLOSED 记录可无限多，OPEN 记录每槽位最多一条
    UNIQUE KEY uk_one_open_per_slot (user_id, contract_code, open_slot),
    KEY idx_user_status (user_id, status),
    CONSTRAINT chk_position_route CHECK (route IN ('STANDARD', 'SYNTHETIC')),
    CONSTRAINT chk_position_status CHECK (status IN ('OPEN', 'CLOSED')),
    CONSTRAINT chk_position_direction CHECK (direction IN ('LONG', 'SHORT'))
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin COMMENT = '合约持仓';

-- 纵深防御：DB 层禁止修改持仓的路由与归属，CLOSED 持仓不可重新打开。
CREATE TRIGGER trg_swap_positions_immutable
    BEFORE UPDATE ON swap_positions
    FOR EACH ROW
BEGIN
    IF NEW.route <> OLD.route OR NEW.user_id <> OLD.user_id OR NEW.contract_code <> OLD.contract_code THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'swap_positions: route/user_id/contract_code are immutable';
    END IF;
    IF OLD.status = 'CLOSED' AND NEW.status <> 'CLOSED' THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'swap_positions: closed position cannot be reopened';
    END IF;
END;

CREATE TABLE IF NOT EXISTS swap_orders (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id         BIGINT          NOT NULL,
    client_order_id VARCHAR(64)     NOT NULL COMMENT '客户端幂等键',
    contract_code   VARCHAR(32)     NOT NULL,
    route           VARCHAR(16)     NOT NULL COMMENT '与所属持仓的 route 一致',
    position_id     BIGINT UNSIGNED NOT NULL,
    side            VARCHAR(4)      NOT NULL COMMENT 'BUY / SELL',
    intent          VARCHAR(8)      NOT NULL COMMENT 'OPEN / INCREASE / REDUCE',
    order_type      VARCHAR(8)      NOT NULL COMMENT 'MARKET / LIMIT',
    volume          DECIMAL(36, 18) NOT NULL,
    price           DECIMAL(36, 18) NOT NULL DEFAULT 0 COMMENT '限价单价格；市价单为 0',
    leverage        INT UNSIGNED    NOT NULL,
    fill_price      DECIMAL(36, 18) NOT NULL DEFAULT 0,
    filled_volume   DECIMAL(36, 18) NOT NULL DEFAULT 0,
    margin_delta    DECIMAL(36, 18) NOT NULL DEFAULT 0 COMMENT '正：冻结；负：释放',
    realized_pnl    DECIMAL(36, 18) NOT NULL DEFAULT 0,
    status          VARCHAR(16)     NOT NULL,
    err_msg         VARCHAR(255)    NULL,
    created_at      DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at      DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uk_user_client_order (user_id, client_order_id),
    KEY idx_position (position_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin COMMENT = '合约订单';
