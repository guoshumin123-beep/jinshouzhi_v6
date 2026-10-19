# /market/history/kline (K线数据（蜡烛图）)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=7ec3ff18-7773-11ed-9966-0242ac110003

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | GET |
| 是否验签 | 否 |
| 接口权限 | - |
| 限频 | 4,500 5 minutes |
| 接口描述 | 此接口返回历史K线数据。K线周期以新加坡时间为基准开始计算，例如日K线的起始周期为新加坡时间0时至新加坡时间次日0时。 |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `https://api.huobi.pro` |
| 线上环境（aws客户首选） | `https://api-aws.huobi.pro` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| symbol | string | 是 | 交易对<br>btcusdt, ethbtc等（如需获取杠杆ETP净值K线，净值symbol = 杠杆ETP交易对symbol + 后缀‘nav’，例如：btc3lusdtnav） |
| period | string | 是 | 返回数据时间粒度，也就是每根蜡烛的时间区间<br>1min, 5min, 15min, 30min, 60min, 4hour, 1day, 1mon, 1week, 1year |
| size | integer | 否 | 返回 K 线数据条数<br>[1-2000] |

## 响应参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| status | string | 否 | 请求处理结果 "ok","error" |
| ch | string | 否 | 数据所属的 channel，格式：market.$symbol.kline.$period |
| ts | long | 否 | 响应生成时间点，单位：毫秒 |
| <data> | object | 否 |  |
| id | long | 否 | 调整为新加坡时间的时间戳，单位秒，并以此作为此K线柱的id |
| amount | float | 否 | 以基础币种计量的交易量 |
| count | integer | 否 | 交易次数 |
| open | float | 否 | 本阶段开盘价 |
| close | float | 否 | 本阶段收盘价 |
| low | float | 否 | 本阶段最低价 |
| high | float | 否 | 本阶段最高价 |
| vol | float | 否 | 以报价币种计量的交易量 |
| </data> |  | 否 |  |

## 请求示例
```http
curl"https://api.huobi.pro/market/history/kline?period=1day&size=200&symbol=btcusdt"
```

## 响应示例
```json
{
"ch":"market.btcusdt.kline.5min",
"status":"ok",
"ts":1629769247172,
"data":[
{
"id":1629769200,
"open":49056.37,
"close":49025.51,
"low":49022.86,
"high":49056.38,
"amount":3.946281917950917,
"vol":193489.67275732,
"count":196
},
{
"id":1629768900,
"open":48994.61,
"close":49056.37,
"low":48966.72,
"high":49072.46,
"amount":30.72223099519689,
"vol":1505870.732227976,
"count":1504
}
]
}
```

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
