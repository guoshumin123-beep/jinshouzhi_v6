# OrderCancelReject <9>

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=5494

当 OrderCancelRequest`` 失败时，由服务器发送的消息。

| Tag | 名称 | 类型 | 是否必须 | 描述 |
|---|---|---|---|---|
| 11 | ClOrdID | String | Y | 来自取消请求的 ClOrdID 。 |
| 41 | OrigClOrdID | String | N | 要取消订单的  client id |
| 37 | OrderID | Int | N | 来自取消请求的 OrderID。 |
| 102 | CxlRejReason | Int | Y | 错误码 |
| 58 | Text | String | Y | 可读的错误消息。 |
