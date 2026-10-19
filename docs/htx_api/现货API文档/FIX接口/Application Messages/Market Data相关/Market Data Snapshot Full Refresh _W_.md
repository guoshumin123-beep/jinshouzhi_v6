# Market Data Snapshot Full Refresh <W>

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=5501

由服务端发送。表示市场mbp 。

| Tag | 名称 | 类型 | 字段对应 | 是否必须 | 描述 |
|---|---|---|---|---|---|
| 262 | MDReqID | String |  | Y | Market Data Request的唯一标识 ，固定值 ：1 |
| 83 | RptSeq | String | seqNum | Y | 撮合序列号，交易对唯一 |
| 55 | Symbol | String | symbol | Y | symbol |
| 268 | NoMDEntries | Int |  | Y |  |
| =>269 | MDEntryType | Char |  | Y | 0=Bid1=Offer |
| =>270 | MDEntryPx | Decimal | price | Y | 价格. |
| =>271 | MDEntrySize | Decimal | size | Y | 数量 |
