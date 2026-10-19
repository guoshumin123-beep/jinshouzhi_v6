# OrderCancelRequest <F>

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=5493

由客户发送的，用以取消订单。

要取消订单，需要 `OrderID`或 `OrigClOrdID``两者必填其一。如果都填优先OrderID`。

| Tag | 名称 | 类型 | 是否必须 | 描述 |
|---|---|---|---|---|
| 11 | ClOrdID | String | Y | 请求ID |
| 41 | OrigClOrdID | String | N | 要取消订单的 client id |
| 37 | OrderID | String | N | 要取消订单的 OrderID。 |

``

响应：

对于每个被取消的订单，ExecutionReport``的 `ExecType`值为 cancellation。

如果取消被拒绝，则为 OrderCancelReject``。

如果消息被拒绝，则为 Reject``。
