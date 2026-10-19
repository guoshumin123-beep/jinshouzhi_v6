# Market Data Incremental Refresh <X>

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=5502

由服务端发送。表示成交数据。

| Tag | 名称 | 类型 | 对应字段 | 是否必须 | 描述 |
|---|---|---|---|---|---|
| 262 | MDReqID | String |  | Y | market data request的唯一标识 ，固定值 ：1 |
| 268 | NoMDEntries | Int |  | Y |  |
| =>269 | MDEntryType | Char |  | Y | 2=Trade |
| =>279 | MDUpdateAction | Int |  | Y | Market Data更新类型. 固定值: 0 = New |
| =>55 | Symbol | String | symbol | Y | symbol |
| =>278 | MDEntryID | String | tradeId | Y | 成交ID |
| =>270 | MDEntryPx | Decimal | price | Y | 价格 |
| =>271 | MDEntrySize | Decimal | amount | Y | 成交数量 |
| =>60 | TransactTime | UTCTimestamp | ts | Y | 成交时间 |
| =>54 | Side | Int | direction | Y | 成交方向 1 = BUY2 = SELL |
