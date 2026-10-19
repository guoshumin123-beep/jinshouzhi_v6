# ExecutionReport <8>

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=5492

每当订单状态发生变化时由服务器发送。同订单推送+清算手续费推送

**注意:**

同资产推送是按照用户维度推送。即监听撮合结果。

ExecutionReport`` 会发送该账户的所有订单，包括在不同连接中提交的订单。

| Tag | 名称 | 类型 | API对应字段 | 是否必须 | 描述 |
|---|---|---|---|---|---|
| 11 | ClOrdID | String | client-order-id | N | 列表中分配的 ClOrdID。 |
| 37 | OrderID | String | orderId | N | 由交易所分配ID。 |
| 38 | OrderQty | Decimal | amount、orderSize | N | 订单数量。 |
| 40 | OrdType | String | type | Y | 可能的值:同 NewOrderSingle ：OrdType |
| 55 | Symbol | String | symbol | N | 订单的交易对。 |
| 54 | Side | String | orderSide | N | 订单方向:1 - BUY2 - SELL |
| 150 | ExecType | String | eventType | N | 事件类型，有效值： 订单创建：creation 订单成交：trade 订单撤销：cancellation 手续费：clearing 拒绝：rejected |
| 60 | TransactTime | UTCTimestamp | lastActTime | N | 此事件发生的时间戳。撮合成功时间 |
| 39 | OrdStatus | String | orderStatus | Y | 订单状态： 1：rejected，2：canceled，3：submitted，4：partial-filled，5：filled，6：partial-canceled,  7：canceled |
| 1 | account | String | accountId | N | accountId |
| 578 | TradeInputSource | String | orderSource | N | orderSource |
| 42 |  | UTCTIMESTAMP | orderCreateTime | N | 订单创建时间 |
| 44 | Price | Decimal | price、orderPrice | N | 订单价格 |
| 103 | OrdRejReason | String | errCode | N | 订单触发失败错误码 |
| 58 | Text | String | errMessage | N | 订单触发失败错误消息 |
| 119 | SettlCurrAmt | Decimal | orderValue | N | 订单金额（仅对市价买单有效） |
| 1003 | TradeID |  | tradeId | N | 成交ID |
| 31 | LastPx | Decimal | tradePrice | N | 成交价格 |
| 32 | LastQty | String | tradeVolume | N | 成交量 |
| 1057 | AggressorIndicator | Boolean | aggressor | N | 是否交易主动方，有效值： true (taker), false (maker) |
| 151 | LeavesQty | Decimal | remainAmt | N | 该订单未成交数量（市价买单为未成交金额） |
| 14 | CumQty | Decimal | execAmt | N | 该订单累计成交量（市价买单为成交金额） |
| 136 | NoMiscFees | Int |  | N | 手续费的重复组数量。 |
| =>137 | MiscFeeAmt | Decimal | transactFee | Y | 以 MiscFeeCurr资产计价的费用金额 |
| =>138 | MiscFeeCurr | String | feeCurrency | Y | 手续费的货币。 |
| =>139 | MiscFeeType | Int |  | Y | 可能的值: 4 - EXCHANGE_FEES |
