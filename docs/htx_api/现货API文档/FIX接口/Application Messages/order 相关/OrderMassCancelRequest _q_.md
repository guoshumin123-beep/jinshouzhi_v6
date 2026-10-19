# OrderMassCancelRequest <q>

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=5495

由客户端发送，用以取消交易品种上的所有挂单。

**注意事项：1.** 该账户的所有订单都将被取消，包括从不同连接中下的订单。

                  2.MassCancelRequestType =1 将忽略Symbol；MassCancelRequestType =2 ，Symbol必须填写

| Tag | 名称 | 类型 | 是否必须 | 描述 |
|---|---|---|---|---|
| 11 | ClOrdID | String | Y | 此批量取消请求的ID 。 |
| 530 | MassCancelRequestType | Int | Y | 1 - 取消所有订单 2- 取消指定交易对的订单 |
| 146 | NoRelatedSym | Int | N | 取消订单的交易对数量 |
| =>55 | Symbol | String | N | 取消订单的交易对 |

**响应：**

对于每个被取消的订单，发送ExecutionReport``

对于请求是否被接受或者拒绝，请参考 OrderMassCancelReport`` 。

如果消息被拒绝，则为 Reject``。
