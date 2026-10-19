-- 合约领域模型 v2：引入保证金模式（与路由画像相互独立）、成交明细、资金费、用户级风控锁。
-- 000002 中的 gateway 表尚未投产且无数据，此处直接重建。

DROP TABLE IF EXISTS swap_orders;
DROP TRIGGER IF EXISTS trg_swap_positions_immutable;
DROP TABLE IF EXISTS swap_positions;
DROP TABLE IF EXISTS position_slots;
DROP TABLE IF EXISTS user_route_profiles;

-- 用户交易画像：route 由 Admin 控制（仅影响未来新开仓）；margin_mode 为用户自选的默认保证金模式。
CREATE TABLE user_trade_profiles (
    user_id          BIGINT      NOT NULL,
    route            VARCHAR(16) NOT NULL DEFAULT 'STANDARD',
    margin_mode      VARCHAR(16) NOT NULL DEFAULT 'CROSS',
    route_updated_by VARCHAR(64) NULL COMMENT '最近一次修改 route 的 Admin',
    route_updated_at DATETIME(6) NULL,
    created_at       DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at       DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (user_id),
    CONSTRAINT chk_profile_route CHECK (route IN ('STANDARD', 'SYNTHETIC')),
    CONSTRAINT chk_profile_margin_mode CHECK (margin_mode IN ('ISOLATED', 'CROSS'))
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin COMMENT = '用户交易画像';

-- 用户级串行化锁：同一用户的下单、撮合成交、强平、资金费、划出全部串行，
-- 全仓风险计算（跨合约、跨保证金模式共享 SWAP 钱包）因此在一致快照上进行。
-- 全局加锁顺序：user_locks -> dealer 系统账户 -> dealer 用户账户。
CREATE TABLE user_locks (
    user_id    BIGINT      NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (user_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin COMMENT = '用户级串行化锁';

CREATE TABLE swap_positions (
    id                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id           BIGINT          NOT NULL,
    contract_code     VARCHAR(32)     NOT NULL,
    margin_mode       VARCHAR(16)     NOT NULL COMMENT '开仓时固化',
    route             VARCHAR(16)     NOT NULL COMMENT '开仓时固化，生命周期内不可修改',
    direction         VARCHAR(8)      NOT NULL COMMENT 'LONG / SHORT；平反手会关闭旧仓并新建反向仓',
    volume            DECIMAL(36, 18) NOT NULL DEFAULT 0 COMMENT '张数',
    avg_price         DECIMAL(36, 18) NOT NULL DEFAULT 0,
    margin            DECIMAL(36, 18) NOT NULL DEFAULT 0 COMMENT '逐仓：SWAP_ISOLATED 中归属本仓的保证金；全仓：0（按标记价实时计算）',
    leverage          INT UNSIGNED    NOT NULL,
    liquidation_price DECIMAL(36, 18) NOT NULL DEFAULT 0 COMMENT '逐仓预估强平价（展示用，风控以实时权益判断为准）',
    realized_pnl      DECIMAL(36, 18) NOT NULL DEFAULT 0,
    fee_paid          DECIMAL(36, 18) NOT NULL DEFAULT 0,
    funding_paid      DECIMAL(36, 18) NOT NULL DEFAULT 0 COMMENT '累计资金费，正数为用户支付',
    status            VARCHAR(8)      NOT NULL COMMENT 'OPEN / CLOSED',
    close_reason      VARCHAR(16)     NULL COMMENT 'CLOSE / FLIP / LIQUIDATION',
    open_slot         TINYINT GENERATED ALWAYS AS (IF(status = 'OPEN', 1, NULL)) STORED,
    opened_at         DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    closed_at         DATETIME(6)     NULL,
    updated_at        DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    -- 单向持仓：每个 (user, contract, margin_mode) 同时最多一个 OPEN 持仓
    UNIQUE KEY uk_one_open_per_slot (user_id, contract_code, margin_mode, open_slot),
    KEY idx_route_status (route, status, contract_code),
    CONSTRAINT chk_position_route CHECK (route IN ('STANDARD', 'SYNTHETIC')),
    CONSTRAINT chk_position_margin_mode CHECK (margin_mode IN ('ISOLATED', 'CROSS')),
    CONSTRAINT chk_position_status CHECK (status IN ('OPEN', 'CLOSED')),
    CONSTRAINT chk_position_direction CHECK (direction IN ('LONG', 'SHORT'))
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin COMMENT = '合约持仓';

CREATE TRIGGER trg_swap_positions_immutable
    BEFORE UPDATE ON swap_positions
    FOR EACH ROW
BEGIN
    IF NEW.route <> OLD.route OR NEW.margin_mode <> OLD.margin_mode OR NEW.direction <> OLD.direction
        OR NEW.user_id <> OLD.user_id OR NEW.contract_code <> OLD.contract_code THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'swap_positions: route/margin_mode/direction/user_id/contract_code are immutable';
    END IF;
    IF OLD.status = 'CLOSED' AND NEW.status <> 'CLOSED' THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'swap_positions: closed position cannot be reopened';
    END IF;
END;

CREATE TABLE swap_orders (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id         BIGINT          NOT NULL,
    client_order_id VARCHAR(64)     NOT NULL COMMENT '客户端幂等键',
    contract_code   VARCHAR(32)     NOT NULL,
    margin_mode     VARCHAR(16)     NOT NULL,
    route           VARCHAR(16)     NOT NULL COMMENT '受理时固化：槽位有持仓/在途订单时继承，否则取画像',
    side            VARCHAR(4)      NOT NULL COMMENT 'BUY / SELL',
    order_type      VARCHAR(8)      NOT NULL COMMENT 'MARKET / LIMIT',
    reduce_only     TINYINT(1)      NOT NULL DEFAULT 0,
    source          VARCHAR(16)     NOT NULL DEFAULT 'USER' COMMENT 'USER / LIQUIDATION',
    volume          DECIMAL(36, 18) NOT NULL,
    price           DECIMAL(36, 18) NOT NULL DEFAULT 0 COMMENT '限价单价格；市价单为 0',
    leverage        INT UNSIGNED    NOT NULL,
    filled_volume   DECIMAL(36, 18) NOT NULL DEFAULT 0,
    avg_fill_price  DECIMAL(36, 18) NOT NULL DEFAULT 0,
    fee_paid        DECIMAL(36, 18) NOT NULL DEFAULT 0,
    realized_pnl    DECIMAL(36, 18) NOT NULL DEFAULT 0,
    frozen_amount   DECIMAL(36, 18) NOT NULL DEFAULT 0 COMMENT 'Synthetic 挂单剩余冻结（保证金 + 预估手续费）',
    fill_seq        INT UNSIGNED    NOT NULL DEFAULT 0 COMMENT '成交批次序号，用于账本幂等键',
    status          VARCHAR(20)     NOT NULL,
    err_msg         VARCHAR(255)    NULL,
    created_at      DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at      DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uk_user_client_order (user_id, client_order_id),
    KEY idx_match (route, contract_code, status, side, price),
    KEY idx_user_slot_active (user_id, contract_code, margin_mode, status)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin COMMENT = '合约订单';

CREATE TABLE swap_trades (
    id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    order_id       BIGINT UNSIGNED NOT NULL,
    user_id        BIGINT          NOT NULL,
    position_id    BIGINT UNSIGNED NOT NULL,
    contract_code  VARCHAR(32)     NOT NULL,
    margin_mode    VARCHAR(16)     NOT NULL,
    route          VARCHAR(16)     NOT NULL,
    side           VARCHAR(4)      NOT NULL,
    kind           VARCHAR(8)      NOT NULL COMMENT 'OPEN / CLOSE',
    role           VARCHAR(12)     NOT NULL COMMENT 'MAKER / TAKER / LIQUIDATION',
    price          DECIMAL(36, 18) NOT NULL,
    volume         DECIMAL(36, 18) NOT NULL,
    fee            DECIMAL(36, 18) NOT NULL DEFAULT 0,
    realized_pnl   DECIMAL(36, 18) NOT NULL DEFAULT 0,
    loss_shortfall DECIMAL(36, 18) NOT NULL DEFAULT 0 COMMENT '穿仓差额（由 SYNTHETIC_POOL 兜底）',
    created_at     DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    KEY idx_order (order_id),
    KEY idx_position (position_id),
    KEY idx_user_time (user_id, created_at)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin COMMENT = '合约成交明细';

CREATE TABLE synthetic_funding_rounds (
    contract_code VARCHAR(32)     NOT NULL,
    funding_time  DATETIME(6)     NOT NULL,
    rate          DECIMAL(36, 18) NOT NULL,
    mark_price    DECIMAL(36, 18) NOT NULL,
    status        VARCHAR(12)     NOT NULL COMMENT 'SETTLING / SETTLED',
    created_at    DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    settled_at    DATETIME(6)     NULL,
    PRIMARY KEY (contract_code, funding_time)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin COMMENT = 'Synthetic 资金费结算轮次';

CREATE TABLE swap_funding_payments (
    position_id   BIGINT UNSIGNED NOT NULL,
    funding_time  DATETIME(6)     NOT NULL,
    user_id       BIGINT          NOT NULL,
    contract_code VARCHAR(32)     NOT NULL,
    rate          DECIMAL(36, 18) NOT NULL,
    mark_price    DECIMAL(36, 18) NOT NULL,
    amount        DECIMAL(36, 18) NOT NULL COMMENT '正数为用户支付',
    shortfall     DECIMAL(36, 18) NOT NULL DEFAULT 0,
    created_at    DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (position_id, funding_time),
    KEY idx_user (user_id, funding_time)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin COMMENT = '持仓资金费明细';
