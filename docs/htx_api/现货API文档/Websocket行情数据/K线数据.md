# market.$symbol$.kline.$period$ (K线数据)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=7ec4953e-7773-11ed-9966-0242ac110003

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | - |
| 是否验签 | 否 |
| 接口权限 | 读取 |
| 接口描述 | 一旦K线数据产生，Websocket服务器将通过此订阅主题接口推送至客户端： |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `wss://api.huobi.pro/ws` |
| 线上环境（aws客户首选） | `wss://api-aws.huobi.pro/ws` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| symbol | Array | 是 | 交易代码<br>btcusdt, ethbtc...等（如需获取杠杆ETP净值K线，净值symbol = 杠杆ETP交易对symbol + 后缀‘nav’，例如：btc3lusdtnav） |
| period | Array | 是 | K线周期<br>1min, 5min, 15min, 30min, 60min, 4hour, 1day, 1mon, 1week, 1year |
| cid | string | 否 | 表示本次请求id |

## 请求示例
```json
{
  "id": "id1",
  "sub": ["market.btcusdt.kline.1min","market.ethusdt.kline.5min","market.solusdt.kline.15min"]
}
```

## 响应示例
```json
{
"id":"id1",
"status":"ok",
"subbed":"market.ethbtc.kline.1min",
"ts":1489474081631
}
```

## 推送数据
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| ch | string | 否 | 数据所属的 channel，格式：market.$symbol.kline.$period |
| ts | long | 否 | 系统响应时间 |
| <tick> | object | 否 |  |
| id | integer | 否 | unix时间，同时作为K线ID |
| amount | float | 否 | 成交量 |
| count | integer | 否 | 成交笔数 |
| open | float | 否 | 开盘价 |
| close | float | 否 | 收盘价（当K线为最晚的一根时，是最新成交价） |
| low | float | 否 | 最低价 |
| high | float | 否 | 最高价 |
| vol | float | 否 | 成交额, 即 sum(每一笔成交价 * 该笔的成交量) |
| </tick> |  | 否 |  |

## 推送示例
```json
{
"ch":"market.ethbtc.kline.1min",
"ts":1630981694370,
"tick":{
"id":1630981680,
"open":0.074849,
"close":0.074848,
"low":0.074848,
"high":0.074849,
"amount":2.4448,
"vol":0.1829884187,
"count":3
}
}
```

## 取消订阅示例
```json
{
  "unsub": "market.ethbtc.kline.1min",
  "id": "id1"
}
```

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
