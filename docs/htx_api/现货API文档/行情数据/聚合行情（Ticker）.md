# /market/detail/merged (聚合行情（Ticker）)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=7ec3fc25-7773-11ed-9966-0242ac110003

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | GET |
| 是否验签 | 否 |
| 接口权限 | - |
| 限频 | 4,500 5 minutes |
| 接口描述 | 此接口获取ticker信息同时提供最近24小时的交易聚合信息。 |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `https://api.huobi.pro` |
| 线上环境（aws客户首选） | `https://api-aws.huobi.pro` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| symbol | string | 是 | 交易对<br>btcusdt, ethbtc...（取值参考GET /v1/common/symbols） |

## 响应参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| status | string | 否 | 请求处理结果 "ok","error" |
| ch | string | 否 | 数据所属的 channel，格式：market.$symbol.detail.merged |
| ts | long | 否 | 响应生成时间点，单位：毫秒 |
| <tick> | object | 否 |  |
| id | long | 否 |  |
| amount | float | 否 | 以基础币种计量的交易量（以滚动24小时计） |
| count | integer | 否 | 交易次数（以滚动24小时计） |
| open | float | 否 | 本阶段开盘价（以滚动24小时计） |
| close | float | 否 | 本阶段最新价（以滚动24小时计） |
| low | float | 否 | 本阶段最低价（以滚动24小时计） |
| high | float | 否 | 本阶段最高价（以滚动24小时计） |
| vol | float | 否 | 以报价币种计量的交易量（以滚动24小时计） |
| bid | object | 否 | 当前的最高买价 [price, size] |
| ask | object | 否 | 当前的最低卖价 [price, size] |
| </tick> |  | 否 |  |

## 请求示例
```http
curl"https://api.huobi.pro/market/detail/merged?symbol=ethusdt"
```

## 响应示例
```json
{
"ch":"market.btcusdt.detail.merged",
"status":"ok",
"ts":1629788763750,
"tick":{
"id":272156789143,
"version":272156789143,
"open":50080.0,
"close":49820.92,
"low":48767.0,
"high":50500.0,
"amount":12055.365781937457,
"vol":5.985618685709001E8,
"count":420573,
"bid":[
49819.48,
2.58112
],
"ask":[
49819.49,
0.002411
]
}
}
```

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
