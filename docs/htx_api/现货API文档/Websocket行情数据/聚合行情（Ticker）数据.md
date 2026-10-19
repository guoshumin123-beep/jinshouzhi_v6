# market.$symbol.ticker (聚合行情（Ticker）数据)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=7ec49d40-7773-11ed-9966-0242ac110003

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | - |
| 是否验签 | 否 |
| 接口权限 | 读取 |
| 接口描述 | 获取市场聚合行情数据，每100ms推送一次。 |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `wss://api.huobi.pro/ws` |
| 线上环境（aws客户首选） | `wss://api-aws.huobi.pro/ws` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| symbol | Array | 是 | 交易对<br>btcusdt, ethbtc...（取值参考GET /v1/common/symbols） |
| cid | string | 否 | 表示本次请求id |

## 请求示例
```json
{
  "sub":["market.btcusdt.mbp.refresh.20","market.ethusdt.mbp.refresh.20","market.htxusdt.mbp.refresh.20"]
}
```

## 响应示例
```json
{
  "id": "id1",
  "status": "ok",
  "subbed": "market.btcusdt.ticker",
  "ts": 1489474081631
}
```

## 推送数据
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| ch | string | 否 | 数据所属的 channel，格式：market.$symbol.ticker |
| ts | long | 否 | 系统响应时间 |
| <tick> | object | 否 |  |
| amount | float | 否 | 以基础币种计量的交易量（以滚动24小时计） |
| count | integer | 否 | 交易次数（以滚动24小时计） |
| open | float | 否 | 本阶段开盘价（以滚动24小时计） |
| close | float | 否 | 本阶段最新价（以滚动24小时计） |
| low | float | 否 | 本阶段最低价（以滚动24小时计） |
| high | float | 否 | 本阶段最高价（以滚动24小时计） |
| vol | float | 否 | 以报价币种计量的交易量（以滚动24小时计） |
| bid | float | 否 | 当前的最高买价 |
| bidSize | float | 否 | 最高买价对应的量 |
| ask | float | 否 | 当前的最低卖价 |
| askSize | float | 否 | 最低卖价对应的量 |
| lastPrice | float | 否 | 最新成交价 |
| lastSize | float | 否 | 最新成交价对应的量 |
| </tick> |  | 否 |  |

## 推送示例
```json
{
"ch":"market.btcusdt.ticker",
"ts":1630982370526,
"tick":{
"open":51732,
"high":52785.64,
"low":51000,
"close":52735.63,
"amount":13259.24137056181,
"vol":687640987.4125315,
"count":448737,
"bid":52732.88,
"bidSize":0.036,
"ask":52732.89,
"askSize":0.583653,
"lastPrice":52735.63,
"lastSize":0.03
}
}
```

## 取消订阅示例
```json
{
  "unsub": "market.btcusdt.ticker"
}
```

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
