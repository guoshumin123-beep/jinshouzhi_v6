# Market Data Request <V>

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=5499

由客户端发送，用以sub、unsub行情数据。sub之后后持续收到Market Data Snapshot Full Refresh和Market Data Incremental Refresh

注意：只支持一次sub 。Unsubscribe将会取消之前的所有Subscribe内容

| Tag | 名称 | 类型 | 是否必须 | 描述 |
|---|---|---|---|---|
| 262 | MDReqID | String | Y | 请求唯一标识ID |
| 263 | SubscriptionRequestType | Int | Y | 0=Subscribe2=Unsubscribe |
| 264 | MarketDepth | Int | Y | Order Book Snapshot深度值 可选的值: 1 , 20 ，150 |
| 146 | NoRelatedSym | Int | Y | 请求的symbol集合。最大值 50 |
| =>55 | Symbol | String | Y | 请求行情数据的symbol名称 |

**响应:**

如果请求被接受， Market Data Incremental Refresh (X) 和 Market Data Snapshot Full Refresh (W)。

如果消息被拒绝，则为 Market Data Request Reject``。
