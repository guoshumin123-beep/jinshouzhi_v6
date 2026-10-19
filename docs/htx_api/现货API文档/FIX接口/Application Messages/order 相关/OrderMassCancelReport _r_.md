# OrderMassCancelReport <r>

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=5496

由服务器发送，用以响应 OrderMassCancelRequest``。

| Tag | 名称 | 类型 | 是否必须 | 描述 |
|---|---|---|---|---|
| 11 | ClOrdID | String | Y | 取消请求中的 ClOrdID 请求ID。 |
| 531 | MassCancelResponse | String | Y | 0:reject 1:accept request |
| 58 | Text | String | N | 可读的错误消息。 |
| 533 | TotalAffectedOrders | Int | N | 取消成功的数量 |
| 534 | NoAffectedOrders | Int | N | 取消失败的数量 |
