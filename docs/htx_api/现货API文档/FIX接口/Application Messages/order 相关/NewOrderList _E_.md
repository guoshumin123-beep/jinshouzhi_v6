# NewOrderList <E>

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=5497

由客户发送，用以提交需要执行的订单列表。

| 73 | NoOrders | Int | Y | Orders 数组中的元素个数。最多20。 |
|---|---|---|---|---|
|  | ..同newSingleOrder |  |  |  |

**响应：**

对于每个执行的订单，发送ExecutionReport``

如果消息被拒绝，则为 Reject``。
