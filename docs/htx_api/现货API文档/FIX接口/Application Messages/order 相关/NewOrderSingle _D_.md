# NewOrderSingle <D>

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=5491

由客户端发送，用以提交新订单并进行执行。

| Tag | 名称 | 类型 | 是否必须 | REST API对应字段 | 描述 |
|---|---|---|---|---|---|
| 11 | ClOrdID | String | Y | client-order-id | 分配给订单的 ClOrdID。 |
| 1 | account | String | Y | account-id |  |
| 38 | OrderQty | Decimal | N | amount | 订单数量 |
| 40 | OrdType | String | Y | type | 订单类型和所需字段。同订单type字段含义 1：buy-market 2：sell-market 3：buy-limit ... |
| 55 | Symbol | String | Y | symbol | 下单的交易对 |
| 44 | Price | Decimal | N | price | 订单价格 |
| 152 | CashOrderQty | Decimal | N | marketAmount | 市价单 金额 |
| 578 | TradeInputSource | String | Y | source | 现货交易填写1:"spot-api" |
| 2362 | SelfMatchPreventionID | Int | N | self-match-prevent | 自成交 ，0：表示允许自成交; 1：表示不允许自成交 |
| 111 | MaxFloor | String | N | ice-amount | 冰山单需要设置 |
| 99 | StopPx | Decimal | N | stop-price | 止盈止损订单触发价格 |
| 1109 | TriggerPriceDirection | String | N | operator | 止盈止损订单触发价运算符 U : >=D :  `的 `ExecType` 值为 creation。

如果订单被拒绝， ExecutionReport``的 `ExecType`值为rejected。

如果消息被拒绝，则为 Reject``。
