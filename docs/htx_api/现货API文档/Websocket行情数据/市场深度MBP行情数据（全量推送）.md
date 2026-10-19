# market.$symbol.mbp.refresh.$levels (市场深度MBP行情数据（全量推送）)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=7ec49c78-7773-11ed-9966-0242ac110003

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | - |
| 是否验签 | 否 |
| 接口权限 | 读取 |
| 接口描述 | 用户可订阅此频道以接收最新深度行情MarketByPrice(MBP)的全量数据推送。推送频率为大约100毫秒一次。 |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `wss://api.huobi.pro/feed` |
| 线上环境（aws客户首选） | `wss://api-aws.huobi.pro/feed` |

**备注**：原本 wss://api.huobi.pro/ws与 wss://api-aws.huobi.pro/ws 地址仍会提供服务，后续可能会下线

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| symbol | Array | 是 | 交易代码（不支持通配符） |
| levels | Array | 是 | 深度档位<br>5,10,20 |
| cid | string | 否 | 表示本次请求id |

## 请求示例
```json
{
  "sub":["market.btcusdt.mbp.refresh.20","market.ethusdt.mbp.refresh.20","market.htxusdt.mbp.refresh.20"],
  "id": "id1"
}
```

## 响应示例
```json
{
"id":"id1",
"status":"ok",
"subbed":"market.btcusdt.mbp.refresh.20",
"ts":1489474081631
}
```

## 推送数据
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| seqNum | integer | 否 | 消息序列号 |
| bids | object | 否 | 买盘，按price降序排列，["price","size"] |
| asks | object | 否 | 卖盘，按price升序排列，["price","size"] |

## 推送示例
```json
{
"ch":"market.btcusdt.mbp.refresh.20",
"ts":"1573199608679",
"tick":{
"seqNum":"100020142010",
"bids":[
[618.37,71.594],
[423.33,77.726],
[223.18,47.997],
[219.34,24.82],
[210.34,94.463]
],
"asks":[
[650.59,14.909733438479636],
[650.63,97.996],
[650.77,97.465],
[651.23,83.973],
[651.42,34.465]
}
}
```

## 取消订阅示例
```json
{
    "unsub": "market.btcusdt.mbp.refresh.20",
    "id": "id1"
}
```

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
