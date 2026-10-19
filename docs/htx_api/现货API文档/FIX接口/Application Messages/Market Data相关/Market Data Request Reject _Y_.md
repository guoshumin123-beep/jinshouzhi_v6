# Market Data Request Reject <Y>

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=5500

由服务端发送。Market Data Request (V)被拒绝时

| Tag | 名称 | 类型 | 是否必须 | 描述 |
|---|---|---|---|---|
| 262 | MDReqID | String | Y | market data request 的唯一标识 |
| 281 | MDReqRejReason | Char | Y | 0=Unknown symbol1=Duplicate MDReqID7=Other |
| 58 | Text | String | N | 错误描述信息 |
